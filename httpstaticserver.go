package main

import (
	"bytes"
	"encoding/json"
	"crypto/md5"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"math/rand"

	"regexp"

	"github.com/go-yaml/yaml"
	"github.com/gorilla/mux"
	"github.com/shogo82148/androidbinary/apk"
)

const YAMLCONF = ".ghs.yml"

type ApkInfo struct {
	PackageName  string `json:"packageName"`
	MainActivity string `json:"mainActivity"`
	Version      struct {
		Code int    `json:"code"`
		Name string `json:"name"`
	} `json:"version"`
}

type IndexFileItem struct {
	Path string
	Info os.FileInfo
}

type HTTPStaticServer struct {
	Root            string
	Upload          bool
	Delete          bool
	Archive         bool
	Title           string
	Theme           string
	PlistProxy      string
	GoogleTrackerID string
	AuthType        string

	indexes []IndexFileItem
	m       *mux.Router
}

func NewHTTPStaticServer(root string) *HTTPStaticServer {
	if root == "" {
		root = "./"
	}
	root = filepath.ToSlash(root)
	if !strings.HasSuffix(root, "/") {
		root = root + "/"
	}
	log.Printf("root path: %s\n", root)
	m := mux.NewRouter()
	s := &HTTPStaticServer{
		Root:  root,
		Theme: "black",
		m:     m,
	}

	go func() {
		time.Sleep(1 * time.Second)
		for {
			startTime := time.Now()
			log.Println("Started making search index")
			s.makeIndex()
			log.Printf("Completed search index in %v", time.Since(startTime))
			//time.Sleep(time.Second * 1)
			time.Sleep(time.Minute * 10)
		}
	}()

	// 暂不支持ipa和apk扫码安装
	// routers for Apple *.ipa
	// m.HandleFunc("/-/ipa/plist/{path:.*}", s.hPlist)
	// m.HandleFunc("/-/ipa/link/{path:.*}", s.hIpaLink)

	m.HandleFunc("/{path:.*}", s.hIndex).Methods("GET", "HEAD")		// HEAD这里只兼容调试，正式环境不会有HEAD
	m.HandleFunc("/{path:.*}", s.hUploadOrMkdir).Methods("POST")
	m.HandleFunc("/{path:.*}", s.hUploadOrMkdir).Methods("PUT")		// 与post一样，唯一区别是可以覆盖已存在的文件，从界面上传默认都为put
	m.HandleFunc("/{path:.*}", s.hDelete).Methods("DELETE")
	return s
}

func (s *HTTPStaticServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.m.ServeHTTP(w, r)
}

func (s *HTTPStaticServer) hIndex(w http.ResponseWriter, r *http.Request) {
	path := mux.Vars(r)["path"]
	if !IsSafePath(path) {
		http.Error(w, "Invalid parent directory accessing.", http.StatusBadRequest)
		return
	}

	relPath := filepath.Join(s.Root, path)
	if r.FormValue("json") == "true" {
		s.hJSONList(w, r)
		return
	}

	if r.FormValue("op") == "info" {
		s.hInfo(w, r)
		return
	}
	if r.FormValue("op") == "checksum" && r.FormValue("checksum-type") == "md5" {
		s.hCalculateMd5(w, r)
		return
	}

	if r.FormValue("op") == "archive" {
		s.hZip(w, r)
		return
	}

	log.Println("GET", path, relPath)
	if r.FormValue("raw") == "false" || isDir(relPath) {
		if r.Method == "HEAD" {
			return
		}
		renderHTML(w, "index.html", s)
	} else {
		if filepath.Base(path) == YAMLCONF {
			auth := s.readAccessConf(path)
			if !auth.Delete {
				http.Error(w, "Security warning, not allowed to read", http.StatusForbidden)
				return
			}
		}
		if r.FormValue("download") == "true" {
			w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(filepath.Base(path)))
		}
		http.ServeFile(w, r, relPath)
	}
}

func (s *HTTPStaticServer) hDelete(w http.ResponseWriter, req *http.Request) {
	path := mux.Vars(req)["path"]
	if !IsSafePath(path) {
		http.Error(w, "Invalid parent directory accessing.", http.StatusBadRequest)
		return
	}

	// s3 multipart uploads handlers
	query := req.URL.Query()
	uploadId := query.Get("uploadId")
	if uploadId != "" {
		s.hS3AbortMultipartUploads(w, req, uploadId)
		return
	}

	// can delete file and directory
	auth := s.readAccessConf(path)
	if !auth.canDelete(req) {
		http.Error(w, "Delete forbidden", http.StatusForbidden)
		return
	}

	// 不允许直接删掉整个根目录
	if path == "/" || path == "" || path == "." {
		http.Error(w, "Unable to delete bucket root.", http.StatusForbidden)
		return
	}

	dst := filepath.Join(s.Root, path)
	err := os.RemoveAll(dst)
	if err != nil {
		pathErr, ok := err.(*os.PathError)
		if ok{
			http.Error(w, pathErr.Op + " " + path + ": " + pathErr.Err.Error(), http.StatusInternalServerError)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPStaticServer) hCalculateMd5(w http.ResponseWriter, req *http.Request) {
	path := mux.Vars(req)["path"]
	if !IsSafePath(path) {
		http.Error(w, "Invalid parent directory accessing.", http.StatusBadRequest)
		return
	}

	dst := filepath.Join(s.Root, path)

	// open file
	f, err := os.Open(dst)
	if err != nil {
		http.Error(w, "", http.StatusNotFound)
	}
	defer f.Close()

	// compute md5
	// 第一版先总是重新计算md5值，不做缓存逻辑
	// 有必要的话再看情况缓存到文件系统
	_t0 := time.Now().UnixNano()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	chkmd5 := fmt.Sprintf("%x", h.Sum(nil))
	_t1 := time.Now().UnixNano()

	w.Header().Set("checksum-type", "md5")
	w.Header().Set("checksum-value", chkmd5)
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"checksum-type": "md5",
			"checksum-value": chkmd5,
			"from-cache": false,
			"time-consumed": float64(_t1 - _t0) / 1000000000,
		},
		"code": http.StatusOK,
	})
}

func (s *HTTPStaticServer) hS3InitiateMultipartUploads(w http.ResponseWriter, req *http.Request) {
	log.Println("handling s3 initiate multipart uploads")

	respInitiateMultipartUploadResultTpl := `
		<?xml version="1.0" encoding="UTF-8"?>
		<InitiateMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
			<Bucket>%s</Bucket>
			<Key>%s</Key>
			<UploadId>%s</UploadId>
		</InitiateMultipartUploadResult>
	`

	bucket := strings.Split(req.Host, ".")[0]
	key := strings.TrimLeft(req.URL.Path, "/")
	uploadId := fmt.Sprintf("%06d", rand.Intn(999999))
	resp := fmt.Sprintf(respInitiateMultipartUploadResultTpl, bucket, key, uploadId)
	resp = strings.TrimSpace(resp)

	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(resp))
}

func (s *HTTPStaticServer) hS3UploadPart(w http.ResponseWriter, req *http.Request, partNumber, uploadId string) {
	log.Println("handling s3 upload part")

	if _, err := strconv.Atoi(partNumber); err != nil || partNumber == "" || uploadId == "" {
		http.Error(w, "Invalid `partNumber` or `uploadId`.", http.StatusBadRequest)
		return
	}

	path := mux.Vars(req)["path"]
	if !IsSafePath(path) {
		http.Error(w, "Invalid parent directory accessing.", http.StatusBadRequest)
		return
	}

	filename := filepath.Base(path)
	dirname := filepath.Dir(path)
	dirpath := filepath.Join(s.Root, dirname)
	partedFilename := fmt.Sprintf(".%s-%s.part-%s", filename, uploadId, partNumber)
	
	if !IsExists(dirpath) {
		if err := os.MkdirAll(dirpath, os.ModePerm); err != nil {
			log.Println("Create directory:", err)
			http.Error(w, "Cannot create directory. " + err.Error(), http.StatusConflict)
			return
		}
	}

	file := req.Body
	if file == nil {
		http.Error(w, "Empty parted upload body.", http.StatusBadRequest)
		return
	}

	dstPath := filepath.Join(dirpath, partedFilename)
	dst, err := os.Create(dstPath)
	if err != nil {
		log.Println("Create file:", err)
		http.Error(w, "File create " + err.Error(), http.StatusConflict)
		return
	}
	if _, err := io.Copy(dst, file); err != nil {
		log.Println("Handle upload file:", err)
		log.Printf("%v %v\n", dstPath, req.Header.Get("Content-Length"))
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	dst.Sync()   // 强制落盘
	dst.Close()  // 主动关闭，不要defer

	dummyEtag := fmt.Sprintf("\"dummy-etag-%06d\"", rand.Intn(999999))

	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("ETag", dummyEtag)
	w.WriteHeader(http.StatusOK)  // Set完所有headers后才能调用WriteHeader
}

func (s *HTTPStaticServer) hS3CompleteMultipartUploads(w http.ResponseWriter, req *http.Request, uploadId string) {
	log.Println("handling s3 complete multipart upload")

	if uploadId == "" {
		http.Error(w, "Invalid empty `uploadId`.", http.StatusBadRequest)
		return
	}

	path := mux.Vars(req)["path"]
	if !IsSafePath(path) {
		http.Error(w, "Invalid parent directory accessing.", http.StatusBadRequest)
		return
	}

	filename := filepath.Base(path)
	dirname := filepath.Dir(path)
	dirpath := filepath.Join(s.Root, dirname)
	partedFilenames := fmt.Sprintf(".%s-%s.part-*", filename, uploadId)

	matches, err := filepath.Glob(filepath.Join(dirpath, partedFilenames))
	if err != nil {
		http.Error(w, "Invalid multipart upload parts. " + err.Error(), http.StatusBadRequest)
		return
	}

	dstPath := filepath.Join(dirpath, filename)
	dst, err := os.Create(dstPath)
	if err != nil {
		log.Println("Create file:", err)
		http.Error(w, "File create " + err.Error(), http.StatusConflict)
		return
	}
	defer dst.Sync()
	defer dst.Close()

	// 逐个part文件合并
	for i := 1; i <= len(matches); i += 1 {
		srcFilename := fmt.Sprintf(".%s-%s.part-%d", filename, uploadId, i)
		srcPath := filepath.Join(dirpath, srcFilename)
		fmt.Printf("[s3-merge] open source parted file: %s\n", srcPath)
		src, err := os.Open(srcPath)
		if err != nil {
			fmt.Printf("[s3-merge err] %s\n", err.Error())
			http.Error(w, err.Error(), http.StatusConflict)   // 不能用timeout，可能会导致客户端自动重发请求，这样会重复merge导致更严重错误
			return
		}
		defer func() {
			if src != nil {
				src.Close()
			}

			// 删除parted files
			os.Remove(srcPath)
		}()

		fmt.Printf("[s3-merge] start copying source parted file: %s\n", srcPath)
		if _, err := io.Copy(dst, src); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		fmt.Printf("[s3-merge] finish copying source parted file: %s\n", srcPath)
	}

	responseTpl := `
		<?xml version="1.0" encoding="UTF-8"?>
		<CompleteMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
			<Location>%s</Location>
			<Bucket>%s</Bucket>
			<Key>%s</Key>
			<ETag>"dummy-etag"</ETag>
		</CompleteMultipartUploadResult>
	`
	scheme := req.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
	}
	bucket := strings.Split(req.Host, ".")[0]
	key := strings.TrimLeft(req.URL.Path, "/")
	location := fmt.Sprintf("%s://%s%s", scheme, req.Host, key)
	resp := fmt.Sprintf(responseTpl, location, bucket, key)
	resp = strings.TrimSpace(resp)

	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(resp))
}

func (s *HTTPStaticServer) hS3AbortMultipartUploads(w http.ResponseWriter, req *http.Request, uploadId string) {
	log.Printf("handling s3 abort multipart upload")

	if uploadId == "" {
		http.Error(w, "Invalid empty `uploadId`.", http.StatusBadRequest)
		return
	}

	path := mux.Vars(req)["path"]
	if !IsSafePath(path) {
		http.Error(w, "Invalid parent directory accessing.", http.StatusBadRequest)
		return
	}

	filename := filepath.Base(path)
	dirname := filepath.Dir(path)
	dirpath := filepath.Join(s.Root, dirname)
	partedFilenames := fmt.Sprintf(".%s-%s.part-*", filename, uploadId)

	matches, err := filepath.Glob(filepath.Join(dirpath, partedFilenames))
	if err != nil {
		http.Error(w, "Invalid multipart upload parts. " + err.Error(), http.StatusBadRequest)
		return
	}

	// 删除parted files
	defer func() {
		// 需要等其他upload线程都彻底断开并释放了文件句柄，不然的话可能会删不掉文件
		// 所以这里等4s，这个sleep不会阻塞其他线程
		time.Sleep(4 * time.Second)
		for _, v := range matches {
			os.Remove(v)
		}
	}()

	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPStaticServer) hUploadOrMkdir(w http.ResponseWriter, req *http.Request) {
	requestMethod := strings.ToUpper(req.Method)
	path := mux.Vars(req)["path"]
	if !IsSafePath(path) {
		http.Error(w, "Invalid parent directory accessing.", http.StatusBadRequest)
		return
	}

	filename := filepath.Base(path)  			// 文件名，会自动忽略掉结尾的"/"
	dirname := filepath.Dir(path)    			// request path中的的directory name
	dirpath := filepath.Join(s.Root, dirname) 	// 实际存储系统中的存储目录

	// 上传不能parseForm，因为会解析整个body，然后body会写入tmpfile，严重影响性能
	// 针对s3的协议，不需要parse body部分，只需要知道query部分
	// 而非s3协议部分，POST/PUT是multipart-form的形式，body是要解析出来的，手动调用 ParseMultipartForm(2<<30) 给2G的缓冲区
	query := req.URL.Query()

	// s3 multipart uploads handlers
	if requestMethod == "POST" {
		if _, exists := query["uploads"]; exists {
			s.hS3InitiateMultipartUploads(w, req)
			return
		}
		uploadId := query.Get("uploadId")
		if uploadId != "" {
			s.hS3CompleteMultipartUploads(w, req, uploadId)
			return
		}
	}
	if requestMethod == "PUT" {
		partNumber := query.Get("partNumber")
		uploadId := query.Get("uploadId")
		if partNumber != "" && uploadId != "" {
			s.hS3UploadPart(w, req, partNumber, uploadId)
			return
		}
	}

	// check auth (ghs standalone auth)
	auth := s.readAccessConf(path)
	if !auth.canUpload(req) {
		http.Error(w, "Upload forbidden", http.StatusForbidden)
		return
	}

	// 1. check filename
	log.Printf("upload filename: %s\n", filename)
	if filename != "" {
		if err := checkFilename(filename); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	dstPath := filepath.Join(dirpath, filename)	// 最终存在文件系统里的文件完整路径

	// POST为非覆盖写
	if requestMethod == "POST" && IsExists(dstPath) {
		w.Header().Set("Content-Type", "application/json;charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":     false,
			"description": "file already exists.",
			"code":			http.StatusConflict,
		})
		return
	}

	// read file body
	var file io.Reader = nil
	contentLength := req.Header.Get("Content-Length")
	if contentLength != "0" && contentLength != "" {
		if !strings.HasPrefix(req.Header.Get("Content-Type"), "multipart/form-data") {
			// file的二进制内容都在body部分
			file = req.Body
		} else {
			// 兼容旧的multipart/form-data的形式
			// 推迟到要读取body的multipar form了才开始解析，并给2G缓冲区
			req.ParseMultipartForm(2 << 30)

			mpFile, _, _ := req.FormFile("file")
			if mpFile != nil {
				defer mpFile.Close()
			}
			if req.MultipartForm != nil {
				defer req.MultipartForm.RemoveAll() // Seen from go source code, req.MultipartForm not nil after call FormFile(..)
			}

			file = mpFile
		}
	}

	// mkdir
	if file == nil {
		// 没有文件的话则表示新建文件夹，
		// request path就是目标文件夹
		dirpath = filepath.Join(s.Root, path)
	}
	if !IsExists(dirpath) {
		if err := os.MkdirAll(dirpath, os.ModePerm); err != nil {
			log.Println("Create directory:", err)
			http.Error(w, "Cannot create directory. " + err.Error(), http.StatusConflict)
			return
		}
	} else {
		// `dirpath` exists and `dirpath` is a file
		dirFi, err := os.Stat(dirpath)
		if err == nil && !dirFi.IsDir() {
			http.Error(w, "Cannot create directory. Target directory is a file.", http.StatusConflict)
			return
		}
	}

	if file == nil {
		// body里没有文件的话，新建完文件夹就可以直接返回了
		// 这部分跟s3无关，仅filesystem的特性
		w.Header().Set("Content-Type", "application/json;charset=utf-8")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":     true,
			"destination": path,
			"makeDirectory": true,
		})
		return
	}

	// 3. write file to disk
	dst, err := os.Create(dstPath)
	if err != nil {
		log.Println("Create file:", err)
		http.Error(w, "File create " + err.Error(), http.StatusConflict)
		return
	}
	defer dst.Sync()
	defer dst.Close()
	if _, err := io.Copy(dst, file); err != nil {
		log.Println("Handle upload file:", err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// response empty body for s3 user agent
	isS3UserAgent, _ := regexp.MatchString("(Boto|aws-sdk-go|S3Manager)", req.Header.Get("User-Agent"))
	if isS3UserAgent {
		return
	}

	w.Header().Set("Content-Type", "application/json;charset=utf-8")

	// disable it temporarily
	// 4. unzip if neccessary
	if false && req.FormValue("unzip") == "true" {
		err = unzipFile(dstPath, dirpath)
		os.Remove(dstPath)
		message := "success"
		if err != nil {
			message = err.Error()
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":     err == nil,
			"description": message,
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     true,
		"destination": path,
	})
}

type FileJSONInfo struct {
	Name    string      `json:"name"`
	Type    string      `json:"type"`
	Size    int64       `json:"size"`
	Path    string      `json:"path"`
	ModTime int64       `json:"mtime"`
	Extra   interface{} `json:"extra,omitempty"`
}

// path should be absolute
func parseApkInfo(path string) (ai *ApkInfo) {
	defer func() {
		if err := recover(); err != nil {
			log.Println("parse-apk-info panic:", err)
		}
	}()
	apkf, err := apk.OpenFile(path)
	if err != nil {
		return
	}
	ai = &ApkInfo{}
	ai.MainActivity, _ = apkf.MainActivity()
	ai.PackageName = apkf.PackageName()
	ai.Version.Code = apkf.Manifest().VersionCode
	ai.Version.Name = apkf.Manifest().VersionName
	return
}

func (s *HTTPStaticServer) hInfo(w http.ResponseWriter, r *http.Request) {
	path := mux.Vars(r)["path"]
	if !IsSafePath(path) {
		http.Error(w, "Invalid parent directory accessing.", http.StatusBadRequest)
		return
	}

	relPath := filepath.Join(s.Root, path)

	fi, err := os.Stat(relPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fji := &FileJSONInfo{
		Name:    fi.Name(),
		Size:    fi.Size(),
		Path:    path,
		ModTime: fi.ModTime().UnixNano() / 1e6,
	}
	ext := filepath.Ext(path)
	switch ext {
	case ".md":
		fji.Type = "markdown"
	case ".apk":
		fji.Type = "apk"
		fji.Extra = parseApkInfo(relPath)
	case "":
		fji.Type = "dir"
	default:
		fji.Type = "text"
	}
	data, _ := json.Marshal(fji)
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (s *HTTPStaticServer) hZip(w http.ResponseWriter, r *http.Request) {
	path := mux.Vars(r)["path"]
	if !IsSafePath(path) {
		http.Error(w, "Invalid parent directory accessing.", http.StatusBadRequest)
		return
	}

	auth := s.readAccessConf(path)
	if !auth.canArchive(r) {
		http.Error(w, "Archive not allowed", http.StatusMethodNotAllowed)
		return
	}
	CompressToZip(w, filepath.Join(s.Root, path))
}

func (s *HTTPStaticServer) hUnzip(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	zipPath, path := vars["zip_path"], vars["path"]
	ctype := mime.TypeByExtension(filepath.Ext(path))
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	err := ExtractFromZip(filepath.Join(s.Root, zipPath), path, w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func combineURL(r *http.Request, path string) *url.URL {
	return &url.URL{
		Scheme: r.URL.Scheme,
		Host:   r.Host,
		Path:   path,
	}
}

func (s *HTTPStaticServer) hPlist(w http.ResponseWriter, r *http.Request) {
	path := mux.Vars(r)["path"]
	if !IsSafePath(path) {
		http.Error(w, "Invalid parent directory accessing.", http.StatusBadRequest)
		return
	}

	// rename *.plist to *.ipa
	if filepath.Ext(path) == ".plist" {
		path = path[0:len(path)-6] + ".ipa"
	}

	relPath := filepath.Join(s.Root, path)
	plinfo, err := parseIPA(relPath)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	baseURL := &url.URL{
		Scheme: scheme,
		Host:   r.Host,
	}
	data, err := generateDownloadPlist(baseURL, path, plinfo)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	w.Write(data)
}

func (s *HTTPStaticServer) hIpaLink(w http.ResponseWriter, r *http.Request) {
	path := mux.Vars(r)["path"]
	if !IsSafePath(path) {
		http.Error(w, "Invalid parent directory accessing.", http.StatusBadRequest)
		return
	}

	var plistUrl string

	if r.URL.Scheme == "https" {
		plistUrl = combineURL(r, "/-/ipa/plist/"+path).String()
	} else if s.PlistProxy != "" {
		httpPlistLink := "http://" + r.Host + "/-/ipa/plist/" + path
		url, err := s.genPlistLink(httpPlistLink)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		plistUrl = url
	} else {
		http.Error(w, "500: Server should be https:// or provide valid plistproxy", 500)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	log.Println("PlistURL:", plistUrl)
	renderHTML(w, "ipa-install.html", map[string]string{
		"Name":      filepath.Base(path),
		"PlistLink": plistUrl,
	})
}

func (s *HTTPStaticServer) genPlistLink(httpPlistLink string) (plistUrl string, err error) {
	// Maybe need a proxy, a little slowly now.
	pp := s.PlistProxy
	if pp == "" {
		pp = defaultPlistProxy
	}
	resp, err := http.Get(httpPlistLink)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	data, _ := ioutil.ReadAll(resp.Body)
	retData, err := http.Post(pp, "text/xml", bytes.NewBuffer(data))
	if err != nil {
		return
	}
	defer retData.Body.Close()

	jsonData, _ := ioutil.ReadAll(retData.Body)
	var ret map[string]string
	if err = json.Unmarshal(jsonData, &ret); err != nil {
		return
	}
	plistUrl = pp + "/" + ret["key"]
	return
}

func (s *HTTPStaticServer) hFileOrDirectory(w http.ResponseWriter, r *http.Request) {
	path := mux.Vars(r)["path"]
	if !IsSafePath(path) {
		http.Error(w, "Invalid parent directory accessing.", http.StatusBadRequest)
		return
	}

	http.ServeFile(w, r, filepath.Join(s.Root, path))
}

type HTTPFileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"`
}

type AccessTable struct {
	Regex string `yaml:"regex"`
	Allow bool   `yaml:"allow"`
}

type UserControl struct {
	Email string
	// Access bool
	Upload bool
	Delete bool
	Token  string
}

type AccessConf struct {
	Upload       bool          `yaml:"upload" json:"upload"`
	Delete       bool          `yaml:"delete" json:"delete"`
	Archive      bool		   `yaml:"archive" json:"archive"`
	Users        []UserControl `yaml:"users" json:"users"`
	AccessTables []AccessTable `yaml:"accessTables"`
}

var reCache = make(map[string]*regexp.Regexp)

func (c *AccessConf) canAccess(fileName string) bool {
	for _, table := range c.AccessTables {
		pattern, ok := reCache[table.Regex]
		if !ok {
			pattern, _ = regexp.Compile(table.Regex)
			reCache[table.Regex] = pattern
		}
		// skip wrong format regex
		if pattern == nil {
			continue
		}
		if pattern.MatchString(fileName) {
			return table.Allow
		}
	}
	return true
}

func (c *AccessConf) canDelete(r *http.Request) bool {
	session, err := store.Get(r, defaultSessionName)
	if err != nil {
		return c.Delete
	}
	val := session.Values["user"]
	if val == nil {
		return c.Delete
	}
	userInfo := val.(*UserInfo)
	for _, rule := range c.Users {
		if rule.Email == userInfo.Email {
			return rule.Delete
		}
	}
	return c.Delete
}

func (c *AccessConf) canArchive(r *http.Request) bool {
	return c.Archive
}

func (c *AccessConf) canUploadByToken(token string) bool {
	for _, rule := range c.Users {
		if rule.Token == token {
			return rule.Upload
		}
	}
	return c.Upload
}

func (c *AccessConf) canUpload(r *http.Request) bool {
	token := r.FormValue("token")
	if token != "" {
		return c.canUploadByToken(token)
	}
	session, err := store.Get(r, defaultSessionName)
	if err != nil {
		return c.Upload
	}
	val := session.Values["user"]
	if val == nil {
		return c.Upload
	}
	userInfo := val.(*UserInfo)

	for _, rule := range c.Users {
		if rule.Email == userInfo.Email {
			return rule.Upload
		}
	}
	return c.Upload
}

func (s *HTTPStaticServer) hJSONList(w http.ResponseWriter, r *http.Request) {
	requestPath := mux.Vars(r)["path"]
	if !IsSafePath(requestPath) {
		http.Error(w, "Invalid parent directory accessing.", http.StatusBadRequest)
		return
	}

	localPath := filepath.Join(s.Root, requestPath)
	search := r.FormValue("search")
	auth := s.readAccessConf(requestPath)
	auth.Upload = auth.canUpload(r)
	auth.Delete = auth.canDelete(r)

	// path string -> info os.FileInfo
	fileInfoMap := make(map[string]os.FileInfo, 0)

	if search != "" {
		results := s.findIndex(search)
		if len(results) > 50 { // max 50
			results = results[:50]
		}
		for _, item := range results {
			if filepath.HasPrefix(item.Path, requestPath) {
				fileInfoMap[item.Path] = item.Info
			}
		}
	} else {
		infos, err := ioutil.ReadDir(localPath)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		for _, info := range infos {
			fileInfoMap[filepath.Join(requestPath, info.Name())] = info
		}
	}

	// turn file list -> json
	lrs := make([]HTTPFileInfo, 0)
	for path, info := range fileInfoMap {
		if !auth.canAccess(info.Name()) {
			continue
		}
		lr := HTTPFileInfo{
			Name:    info.Name(),
			Path:    path,
			ModTime: info.ModTime().UnixNano() / 1e6,
		}
		if search != "" {
			name, err := filepath.Rel(requestPath, path)
			if err != nil {
				log.Println(requestPath, path, err)
			}
			lr.Name = filepath.ToSlash(name) // fix for windows
		}
		if info.IsDir() {
			name := deepPath(localPath, info.Name())
			lr.Name = name
			lr.Path = filepath.Join(filepath.Dir(path), name)
			lr.Type = "dir"
			lr.Size = s.historyDirSize(lr.Path)
		} else {
			lr.Type = "file"
			lr.Size = info.Size() // formatSize(info)
		}
		lrs = append(lrs, lr)
	}

	data, _ := json.Marshal(map[string]interface{}{
		"files": lrs,
		"auth":  auth,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

var dirSizeMap = make(map[string]int64)

func (s *HTTPStaticServer) makeIndex() error {
	var indexes = make([]IndexFileItem, 0)
	var err = filepath.Walk(s.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("WARN: Visit path: %s error: %v", strconv.Quote(path), err)
			return filepath.SkipDir
			// return err
		}
		if info.IsDir() {
			return nil
		}

		path, _ = filepath.Rel(s.Root, path)
		path = filepath.ToSlash(path)
		indexes = append(indexes, IndexFileItem{path, info})
		return nil
	})
	s.indexes = indexes
	dirSizeMap = make(map[string]int64)
	return err
}

func (s *HTTPStaticServer) historyDirSize(dir string) int64 {
	var size int64
	if size, ok := dirSizeMap[dir]; ok {
		return size
	}
	for _, fitem := range s.indexes {
		if filepath.HasPrefix(fitem.Path, dir) {
			size += fitem.Info.Size()
		}
	}
	dirSizeMap[dir] = size
	return size
}

func (s *HTTPStaticServer) findIndex(text string) []IndexFileItem {
	ret := make([]IndexFileItem, 0)
	for _, item := range s.indexes {
		ok := true
		// search algorithm, space for AND
		for _, keyword := range strings.Fields(text) {
			needContains := true
			if strings.HasPrefix(keyword, "-") {
				needContains = false
				keyword = keyword[1:]
			}
			if keyword == "" {
				continue
			}
			ok = (needContains == strings.Contains(strings.ToLower(item.Path), strings.ToLower(keyword)))
			if !ok {
				break
			}
		}
		if ok {
			ret = append(ret, item)
		}
	}
	return ret
}

func (s *HTTPStaticServer) defaultAccessConf() AccessConf {
	return AccessConf{
		Upload: s.Upload,
		Delete: s.Delete,
		Archive: s.Archive,
	}
}

func (s *HTTPStaticServer) readAccessConf(requestPath string) (ac AccessConf) {
	if s.AuthType == "oauth2-proxy" {
		ac = s.defaultAccessConf()
		return
	}

	requestPath = filepath.Clean(requestPath)
	if requestPath == "/" || requestPath == "" || requestPath == "." {
		ac = s.defaultAccessConf()
	} else {
		parentPath := filepath.Dir(requestPath)
		ac = s.readAccessConf(parentPath)
	}
	relPath := filepath.Join(s.Root, requestPath)
	if isFile(relPath) {
		relPath = filepath.Dir(relPath)
	}
	cfgFile := filepath.Join(relPath, YAMLCONF)
	data, err := ioutil.ReadFile(cfgFile)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.Printf("Err read .ghs.yml: %v", err)
	}
	err = yaml.Unmarshal(data, &ac)
	if err != nil {
		log.Printf("Err format .ghs.yml: %v", err)
	}
	return
}

func deepPath(basedir, name string) string {
	isDir := true
	// loop max 5, incase of for loop not finished
	maxDepth := 5
	for depth := 0; depth <= maxDepth && isDir; depth += 1 {
		finfos, err := ioutil.ReadDir(filepath.Join(basedir, name))
		if err != nil || len(finfos) != 1 {
			break
		}
		if finfos[0].IsDir() {
			name = filepath.ToSlash(filepath.Join(name, finfos[0].Name()))
		} else {
			break
		}
	}
	return name
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsDir()
}

func assetsContent(name string) string {
	fd, err := Assets.Open(name)
	if err != nil {
		panic(err)
	}
	data, err := ioutil.ReadAll(fd)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// TODO: I need to read more abouthtml/template
var (
	funcMap template.FuncMap
)

func init() {
	funcMap = template.FuncMap{
		"title": strings.Title,
		"urlhash": func(path string) string {
			httpFile, err := Assets.Open(path)
			if err != nil {
				return path + "#no-such-file"
			}
			info, err := httpFile.Stat()
			if err != nil {
				return path + "#stat-error"
			}
			return fmt.Sprintf("%s?t=%d", path, info.ModTime().Unix())
		},
	}
}

var (
	_tmpls = make(map[string]*template.Template)
)

func executeTemplate(w http.ResponseWriter, name string, v interface{}) {
	if t, ok := _tmpls[name]; ok {
		t.Execute(w, v)
		return
	}
	t := template.Must(template.New(name).Funcs(funcMap).Delims("[[", "]]").Parse(assetsContent(name)))
	_tmpls[name] = t
	t.Execute(w, v)
}

func renderHTML(w http.ResponseWriter, name string, v interface{}) {
	if _, ok := Assets.(http.Dir); ok {
		log.Println("Hot load", name)
		t := template.Must(template.New(name).Funcs(funcMap).Delims("[[", "]]").Parse(assetsContent(name)))
		t.Execute(w, v)
	} else {
		executeTemplate(w, name, v)
	}
}

func checkFilename(name string) error {
	if name == "" {
		return errors.New("Invalid empty filename")
	}
	if name == "." || name == ".." {
		return errors.New("Name can not be \".\" or \"..\". Perhaps you are missing filename in request path.")
	}
	if strings.ContainsAny(name, "\\/") {
		return errors.New("Name should not be empty or contains \\/")
	}
	return nil
}

func IsExists(path string) bool {
	_, err := os.Stat(path)    //os.Stat获取文件信息
	if err != nil {
		if os.IsExist(err) {
			return true
		}
		return false
	}
	return true
}

func IsSafePath(path string) bool {
	if path == "" || path == "/" {
		return true
	}

	// 任意包含访问parent 目录的path都不安全，ban掉
	if strings.HasPrefix("../", path) {
		return false
	}
	matched, _ := regexp.MatchString(`[\/\\]\.{1,2}[\/\\]`, path)
	return !matched
}
