jQuery('#qrcodeCanvas').qrcode({
    text: "http://jetienne.com/"
});

Dropzone.autoDiscover = false;

function getExtention(fname) {
    return fname.slice((fname.lastIndexOf(".") - 1 >>> 0) + 2);
}

function pathJoin(parts, sep) {
    var separator = sep || '/';
    var replace = new RegExp(separator + '{1,}', 'g');
    return parts.join(separator).replace(replace, separator);
}

function getQueryString(name) {
    var reg = new RegExp("(^|&)" + name + "=([^&]*)(&|$)");
    var r = decodeURI(window.location.search).substr(1).match(reg);
    if (r != null) return r[2].replace(/\+/g, ' ');
    return null;
}

function checkPathNameLegal(name) {
    var reg = new RegExp("[\\/]");
    var r = name.match(reg)
    return r == null;
}

function showErrorMessage(jqXHR) {
    let errMsg = jqXHR.getResponseHeader("x-auth-authentication-message")
    if (errMsg == null) {
        errMsg = jqXHR.responseText
    }
    alert(String(jqXHR.status).concat(": ", errMsg));
    console.error(errMsg)
}

var getqs = function (a) {
    a = a || window.location.search
    a = a.substr(1).split('&')

    // --> [] == ""
    if (a == "") return {};
    var b = {};
    for (var i = 0; i < a.length; ++i) {
        var p = a[i].split('=', 2);
        if (p.length == 1)
            b[p[0]] = "";
        else
            b[p[0]] = decodeURIComponent(p[1].replace(/\+/g, " "));
    }
    return b;
}

//是否开启分块上传
window.S3_MULTIPART_UPLOAD_ENABLED = false

//每个文件块的大小(bytes)
window.S3_MULTIPART_UPLOAD_CHUNK_SIZE = 8 * (1 << 20)

//分块上传的最大线程数
window.S3_MULTIPART_UPLOAD_CONCURRENCY = 3

//分块上传最少分块数(按照S3_MULTIPART_UPLOAD_CHUNK_SIZE大小分块后的数量)
window.S3_MULTIPART_UPLOAD_THRESHOLD_SIZE = 3


/**
 * 文件分块上传
 * @param file 分块上传的文件
 * @param dropzoneObj dropzoned对象
 * @param fileXhr dropzone给每个file生成的XmlHttpRequest对象
 * @returns {boolean} 是否不满足分块上传条件，而使用dropzone
 */
function multipartUploadFile(file, dropzoneObj, fileXhr) {
    if (!window.S3_MULTIPART_UPLOAD_ENABLED) return true
    var chunks = new FileUtil().sliceFile(file, window.S3_MULTIPART_UPLOAD_CHUNK_SIZE)
    if (chunks.length <= window.S3_MULTIPART_UPLOAD_THRESHOLD_SIZE) {
        return true
    }
    var needAbortUpload = false
    var fullPath = file.fullPath == undefined ? file.name : file.fullPath
    var fileMultipartUploadApi = new FileMultipartUploadApi({
        filePath: pathJoin([location.pathname, encodeURIComponent(fullPath)]),
    })
    initUploadPart()

    //初始化文件分块上传
    function initUploadPart() {
        var onError = handleError(1000)
        fileMultipartUploadApi.initUpload().then((function (data) {
            this.uploadId = this.getParamFromXml(data, 'UploadId')
            file.status = Dropzone.UPLOADING
            if (this.uploadId == null || this.uploadId === "") {
                var msg = '初始化文件上传请求成功，但未获得正确uploadId。文件 ' + fullPath
                onError({status: -1}, '', msg)
            } else {
                doUploadFilePart(window.S3_MULTIPART_UPLOAD_CONCURRENCY,
                    handleProgress(),
                    handleComplete,
                    handleCancel(),
                    onError)
            }
        }).bind(fileMultipartUploadApi), function (jqXhr, textStatus, errorThorwn) {
            var msg = '文件上传初始化出错。文件 ' + fullPath
            onError(jqXhr, errorThorwn, msg)
        })
    }

    /**
     * 分块上传操作
     * @param threadNum 线程数
     * @param onProgress 上传线程回调函数
     * @param onComplete 上传完成回调函数
     * @param onCancel 上传中止回调函数
     * @param onError 上传出错回调函数
     */
    function doUploadFilePart(threadNum, onProgress, onComplete, onCancel, onError) {
        var taskQueue = []
        var ajaxMap = {}
        var dtdMap = {}

        //对于多线程来讲需要加SyncLock。
        var getSendChunkIndex = function () {
            if (taskQueue.length > 0)
                return taskQueue.shift()
            return -1
        }
        var completeUpload = function (dtdList, chunks) {
            $.when.apply(null, dtdList).then((function () {
                var data = chunks.map(function (value) {
                    return {
                        PartNumber: value.partNumber,
                        ETag: value.etag
                    }
                })
                this.completeMultipartUpload({
                    data: data
                }).then(function (res) {
                    onComplete(res)
                }, function (jqXhr, textStatus, errorThorwn) {
                    var msg = '合并文件 ' + fullPath + ' 出错。'
                    onError(jqXhr, errorThorwn, msg, null, onCancel)
                })
            }).bind(fileMultipartUploadApi))
        }
        var sendChunk = function (index) {
            if (file.status === Dropzone.CANCELED
                || file.status === Dropzone.ERROR
                || needAbortUpload) {
                for (var key in ajaxMap) {
                    ajaxMap[key].abort()
                }
                onCancel()
                return
            }
            if (0 <= index && index < chunks.length) {
                var ajaxObj = {obj: null}
                dtdMap[index] = fileMultipartUploadApi.uploadPart({
                    contentLength: chunks[index].length,
                    partNumber: index + 1,
                    data: chunks[index].fileBlob,
                    onprogress: function (e) {
                        onProgress(chunks, index, e)
                    }
                }, ajaxObj).then(function (data, respHeaderMap) {
                    chunks[index].partNumber = index + 1
                    chunks[index].etag = respHeaderMap.etag
                    sendChunk(getSendChunkIndex())
                    delete ajaxMap[index]
                    delete dtdMap[index]
                }, function (jqXhr, textStatus, errorThorwn) {
                    var msg = '上传文件 ' + fullPath + ' 出错。'
                    onError(jqXhr, errorThorwn, msg, function () {
                        sendChunk(index)
                    }, onCancel)
                })
                ajaxMap[index] = ajaxObj.obj
                if (taskQueue.length === 0) {
                    var dtdList = [];
                    for (var i in dtdMap) {
                        dtdList.push(dtdMap[i])
                    }
                    completeUpload(dtdList, chunks)
                }
            }
        }
        var init = function () {
            chunks.forEach(function (item, index) {
                taskQueue.push(index)
            })
            while (threadNum-- > 0) {
                sendChunk(getSendChunkIndex())
            }
        }
        init()
    }

    var totalSize = file.size

    function handleProgress() {
        var preTime = new Date().getTime()
        var preSendedBytes = 0;
        return function (chunks, chunkIndex, e) {
            chunks[chunkIndex].loaded = e.loaded
            var sendedBytes = chunks.reduce(function (acc, chunk) {
                acc += chunk.loaded === undefined ? 0 : chunk.loaded
                return acc
            }, 0)
            var nowMs = new Date().getTime()
            if (nowMs - preTime >= 500) {
                var sendSize = sendedBytes - preSendedBytes
                var rate = sendSize / (nowMs - preTime) * 1000
                var displayRate = (rate / 1024 / 1024).toFixed(2) + " MB/s"
                var $dzRate = $(".ghs-dz-rate", file.previewElement)
                if ($dzRate.length > 0) {
                    $dzRate.text(displayRate)
                }
                preSendedBytes = sendedBytes
                preTime = nowMs
            }
            fileXhr.upload.onprogress({
                loaded: sendedBytes,
                total: totalSize
            })
        }
    }

    function handleComplete(res) {
        dropzoneObj._finished([file], res)
    }

    function handleCancel() {
        var sendAbortSuccess = false

        function doAbort() {
            if (sendAbortSuccess) return
            sendAbortSuccess = true
            fileMultipartUploadApi.abortMultipartUpload().fail(
                function (jqXhr, textStatus, errorThorwn) {
                    if (jqXhr.readyState === 4) {
                        //请求到达服务器并收到响应
                        switch (jqXhr.status) {
                            case 408://请求超时
                                setTimeout(function () {
                                    sendAbortSuccess = false
                                    doAbort()
                                }, 1000)
                                break
                        }
                    }
                    console.error(textStatus, errorThorwn)
                })
        }

        return doAbort
    }

    function handleError(timeout) {
        var hasCancel = false

        function cancelUpload(onCancel, msg, err, jqXhr) {
            hasCancel = true
            file.status = Dropzone.ERROR
            needAbortUpload = true
            onCancel && onCancel()
            dropzoneObj._errorProcessing([file], msg, jqXhr)
            console.error(msg, err)
        }

        return function (jqXhr, err, msg, callback, onCancel) {
            if (hasCancel) return
            if (jqXhr.status !== 200) {
                if (jqXhr.readyState < 4) {
                    //网络中断请求未到达服务器
                    cancelUpload(onCancel, msg, err, jqXhr)
                } else {
                    //请求到达服务器并收到响应
                    switch (jqXhr.status) {
                        case 408://请求超时
                            if (callback) setTimeout(callback, timeout)
                            else cancelUpload(onCancel, msg, err, jqXhr)
                            break
                        default:
                            cancelUpload(onCancel, msg, err, jqXhr)
                    }
                }
            }
        }
    }

    return false
}

var vm = new Vue({
    el: "#app",
    data: {
        user: {
            email: "",
            name: "",
        },
        location: window.location,
        breadcrumb: [],
        showHidden: false,
        previewMode: false,
        preview: {
            filename: '',
            filetype: '',
            filesize: 0,
            contentHTML: '',
        },
        version: "loading",
        mtimeTypeFromNow: false, // or fromNow
        auth: {},
        search: getQueryString("search"),
        files: [{
            name: "loading ...",
            path: "",
            size: "...",
            type: "dir",
        }],
        myDropzone: null,
        filenameSortBy: '',  // ENUM: {'', 'asc', 'desc'}
    },
    computed: {
        computedFiles: function () {
            var that = this;
            that.preview.filename = null;

            var files = this.files.filter(function (f) {
                if (f.name == 'README.md') {
                    that.preview.filename = f.name;
                }
                if (!that.showHidden && f.name.slice(0, 1) === '.') {
                    return false;
                }
                return true;
            });
            // console.log(this.previewFile)
            if (this.preview.filename) {
                var name = this.preview.filename; // For now only README.md
                console.log(pathJoin([location.pathname, 'README.md']))
                $.ajax({
                    url: pathJoin([location.pathname, 'README.md']),
                    method: 'GET',
                    success: function (res) {
                        var converter = new showdown.Converter({
                            tables: true,
                            omitExtraWLInCodeBlocks: true,
                            parseImgDimensions: true,
                            simplifiedAutoLink: true,
                            literalMidWordUnderscores: true,
                            tasklists: true,
                            ghCodeBlocks: true,
                            smoothLivePreview: true,
                            simplifiedAutoLink: true,
                            strikethrough: true,
                        });

                        var html = converter.makeHtml(res);
                        that.preview.contentHTML = html;
                    },
                    error: function (err) {
                        console.log(err)
                    }
                })
            }

            return files;
        },
    },
    created: function () {
        $.ajax({
            url: "/-/user",
            method: "get",
            dataType: "json",
            success: function (ret) {
                if (ret) {
                    this.user.email = ret.email;
                    this.user.name = ret.name;
                }
            }.bind(this)
        })
        this.myDropzone = new Dropzone("#upload-form", {
            method: 'put',
            parallelUploads: 4,
            paramName: "file",
            maxFilesize: 16384,  // 单文件最大16GiB
            addRemoveLinks: true,
            init: function () {
                //设置要上传的文件的url为path + file.name
                (this.options.url = function (f) {
                    var fullPath = f[0].fullPath == undefined ? f[0].name : f[0].fullPath
                    return pathJoin([location.pathname, encodeURIComponent(fullPath)])
                }).bind(this)
                this.options.headers = {
                    'Accept': '*/*',
                }
                this.on("sending", (function (file, xhr) {
                    file._startTs = new Date().getTime()
                    var $dzFilename = $(".dz-filename", file.previewElement)
                    $('<div class="ghs-dz-rate" style="font-size:12px;"/>').insertAfter($dzFilename)
                    file._instanceId = Math.random().toString()
                    this.isDropzone = multipartUploadFile(file, this, xhr)
                    var _send = xhr.send;
                    xhr.send = (function () {
                        if (this.isDropzone)
                            _send.call(xhr, file);
                    }).bind(this);
                }).bind(this))
                var prevBytesSent = {}  // file._instanceId -> bytes
                this.on("uploadprogress", (function (file, progress) {
                    if (this.isDropzone) {
                        var nowMs = new Date().getTime()
                        var bytesSent = file.upload.bytesSent
                        var prevSent = prevBytesSent[file._instanceId] || [0, 0]
                        var dBytes = bytesSent - prevSent[0]
                        var rate = dBytes / (nowMs - prevSent[1]) * 1000
                        prevBytesSent[file._instanceId] = [bytesSent, nowMs]
                        var displayRate = (rate / 1024 / 1024).toFixed(2) + " MB/s"
                        var $dzRate = $(".ghs-dz-rate", file.previewElement)
                        if ($dzRate.length > 0) {
                            $dzRate.text(displayRate)
                        }
                    }
                }).bind(this));
                this.on("complete", (function (file) {
                    if (!this.isDropzone && file.status === Dropzone.ERROR) return
                    loadFileList()
                    delete prevBytesSent[file._instanceId]
                    var $dzRate = $(".ghs-dz-rate", file.previewElement)
                    if ($dzRate.length > 0) {
                        var nowMs = new Date().getTime()
                        var rate = file.size / (nowMs - file._startTs) * 1000 / 1024 / 1024
                        var displayRate = "avg " + rate.toFixed(2) + " MB/s"
                        $dzRate.text(displayRate)
                    }
                }).bind(this))
            }
        });
    },
    methods: {
        formatTime: function (timestamp) {
            var m = moment(timestamp);
            if (this.mtimeTypeFromNow) {
                return m.fromNow();
            }
            return m.format('YYYY-MM-DD HH:mm:ss');
        },
        toggleHidden: function () {
            this.showHidden = !this.showHidden;
        },
        removeAllUploads: function () {
            this.myDropzone.removeAllFiles();
        },
        parentDirectory: function (path) {
            return path.replace('\\', '/').split('/').slice(0, -1).join('/')
        },
        changeParentDirectory: function (path) {
            var parentDir = this.parentDirectory(path);
            loadFileOrDir(parentDir);
        },
        genInstallURL: function (name, noEncode) {
            var parts = [location.host];
            var pathname = decodeURI(location.pathname);
            if (!name) {
                parts.push(pathname);
            } else if (getExtention(name) == "ipa") {
                parts.push("/-/ipa/link", pathname, name);
            } else {
                parts.push(pathname, name);
            }
            var urlPath = location.protocol + "//" + pathJoin(parts);
            return noEncode ? urlPath : encodeURI(urlPath);
        },
        genQrcode: function (name, title) {
            var urlPath = this.genInstallURL(name, true);
            $("#qrcode-title").html(title || name || location.pathname);
            $("#qrcode-link").attr("href", urlPath);
            $('#qrcodeCanvas').empty().qrcode({
                text: encodeURI(urlPath),
            });

            $("#qrcodeRight a").attr("href", urlPath);
            $("#qrcode-modal").modal("show");
        },
        genDownloadURL: function (f) {
            var search = location.search;
            var sep = search == "" ? "?" : "&"
            return location.origin + "/" + f.path + location.search + sep + "download=true";
        },
        shouldHaveQrcode: function (name) {
            return ['apk', 'ipa'].indexOf(getExtention(name)) !== -1;
        },
        genFileClass: function (f) {
            if (f.type == "dir") {
                if (f.name == '.git') {
                    return 'fa-git-square';
                }
                return "fa-folder-open";
            }
            var ext = getExtention(f.name);
            switch (ext) {
                case "go":
                case "py":
                case "js":
                case "java":
                case "c":
                case "cpp":
                case "h":
                    return "fa-file-code-o";
                case "pdf":
                    return "fa-file-pdf-o";
                case "zip":
                    return "fa-file-zip-o";
                case "mp3":
                case "wav":
                    return "fa-file-audio-o";
                case "jpg":
                case "png":
                case "gif":
                case "jpeg":
                case "tiff":
                    return "fa-file-picture-o";
                case "ipa":
                case "dmg":
                    return "fa-apple";
                case "apk":
                    return "fa-android";
                case "exe":
                    return "fa-windows";
            }
            return "fa-file-text-o"
        },
        clickFileOrDir: function (f, e) {
            // f.name可能是压缩目录的形式如 a/b/c，
            // 所以需要对f.name先split('/')了再encodeURIComponent
            var fName = f.name.split('/').map(encodeURIComponent).join('/')
            var reqPath = pathJoin([location.pathname, fName]);
            if (f.type == "file") {
                location.href = reqPath;
                return true;
            }
            loadFileOrDir(reqPath);
            e.preventDefault()
        },
        changePath: function (reqPath, e) {
            // breadcrumb上的快速path navi
            reqPath = reqPath.split('/').map(encodeURIComponent).join('/')
            loadFileOrDir(reqPath);
            e.preventDefault()
        },
        showInfo: function (f) {
            var data = $.extend(getqs(location.search), {op: "info"});
            $.ajax({
                url: pathJoin(["/", location.pathname, encodeURIComponent(f.name)]),
                data: data,
                method: "GET",
                success: function (res) {
                    $("#file-info-title").text(f.name);
                    $("#file-info-content").text(JSON.stringify(res, null, 2));
                    $("#file-info-modal").modal("show");
                    // console.log(JSON.stringify(res, null, 4));
                },
                error: function (jqXHR, textStatus, errorThrown) {
                    showErrorMessage(jqXHR)
                }
            })
        },
        showChecksumMd5: function (f) {
            var data = $.extend(getqs(location.search), {op: "checksum", "checksum-type": "md5"});
            $.ajax({
                url: pathJoin(["/", location.pathname, encodeURIComponent(f.name)]),
                data: data,
                method: "GET",
                success: function (res) {
                    $("#file-info-title").text(f.name);
                    $("#file-info-content").text(JSON.stringify(res, null, 2));
                    $("#file-info-modal").modal("show");
                },
                error: function (jqXHR, textStatus, errorThrown) {
                    showErrorMessage(jqXHR)
                }
            })
        },
        makeDirectory: function () {
            var name = window.prompt("current path: " + decodeURIComponent(location.pathname) + "\nplease enter a new directory name", "")
            console.log(name)
            if (!name) {
                return
            }
            if (!checkPathNameLegal(name)) {
                alert("Name should not contains any of \\/")
                return
            }
            $.ajax({
                url: pathJoin(["/", location.pathname, encodeURIComponent(name)]),
                method: "POST",
                success: function (res) {
                    console.log(res)
                    loadFileList()
                },
                error: function (jqXHR, textStatus, errorThrown) {
                    showErrorMessage(jqXHR)
                }
            })
        },
        deletePathConfirm: function (f, e) {
            console.log(f)
            e.preventDefault();
            if (!window.confirm('Delete "/' + f.path.replace(/\\/g, '/') + '" ?')) {
                return;
            }

            $.ajax({
                url: pathJoin([location.pathname, encodeURIComponent(f.name)]),
                method: 'DELETE',
                success: function (res) {
                    loadFileList()
                },
                error: function (jqXHR, textStatus, errorThrown) {
                    showErrorMessage(jqXHR)
                }
            });
        },
        updateBreadcrumb: function (pathname) {
            var pathname = pathname || location.pathname || "/";
            pathname = pathname.split('?')[0]
            var parts = pathname.split('/');
            parts = parts.map(function (p) {
                return decodeURIComponent(p)
            })
            this.breadcrumb = [];
            if (pathname == "/") {
                return this.breadcrumb;
            }
            var i = 2;
            for (; i <= parts.length; i += 1) {
                var name = parts[i - 1];
                if (!name) {
                    continue;
                }
                var path = parts.slice(0, i).join('/');
                this.breadcrumb.push({
                    name: name + (i == parts.length ? ' /' : ''),
                    path: path
                })
            }
            return this.breadcrumb;
        },
        handleSortByFilename: function () {
            if (this.filenameSortBy === '') {
                this.filenameSortBy = 'asc'
                this.files = this.files.sort(function (a, b) {
                    if (a.type === 'dir' && b.type !== 'dir') {
                        return -1
                    }
                    if (b.type === 'dir' && a.type !== 'dir') {
                        return 1
                    }
                    return a.name.toLocaleLowerCase().localeCompare(b.name.toLocaleLowerCase())
                })
            } else if (this.filenameSortBy === 'asc') {
                this.filenameSortBy = 'desc'
                this.files = this.files.sort(function (a, b) {
                    if (a.type === 'dir' && b.type !== 'dir') {
                        return -1
                    }
                    if (b.type === 'dir' && a.type !== 'dir') {
                        return 1
                    }
                    return -a.name.toLocaleLowerCase().localeCompare(b.name.toLocaleLowerCase())
                })
            } else {
                // acs -- desc -- <default> 循环
                // <default> 按modtime排序
                this.filenameSortBy = ''
                this.files = this.files.sort(function (a, b) {
                    if (a.type === 'dir' && b.type !== 'dir') {
                        return -1
                    }
                    if (b.type === 'dir' && a.type !== 'dir') {
                        return 1
                    }
                    return b.mtime - a.mtime
                })
            }
        },

        loadAll: function () {
            // TODO: move loadFileList here
        },
    }
})

window.onpopstate = function (event) {
    if (location.search.match(/\?search=/)) {
        location.reload();
        return;
    }
    loadFileList()
}

function loadFileOrDir(reqPath) {
    let requestUri = reqPath + location.search
    var retObj = loadFileList(requestUri)
    if (retObj !== null) {
        retObj.done(function () {
            window.history.pushState({}, "", requestUri);
        });
    }

}

function loadFileList(pathname) {
    var pathname = pathname || location.pathname + location.search;
    var retObj = null
    if (getQueryString("raw") !== "false") { // not a file preview
        var sep = pathname.indexOf("?") === -1 ? "?" : "&"
        retObj = $.ajax({
            url: pathname + sep + "json=true",
            dataType: "json",
            cache: false,
            success: function (res) {
                res.files = res.files.sort(function (a, b) {
                    if (a.type === 'dir' && b.type !== 'dir') {
                        return -1
                    }
                    if (b.type === 'dir' && a.type !== 'dir') {
                        return 1
                    }
                    return b.mtime - a.mtime
                })
                vm.files = res.files;
                vm.auth = res.auth;
                vm.updateBreadcrumb(pathname);
            },
            error: function (jqXHR, textStatus, errorThrown) {
                showErrorMessage(jqXHR)
            },
        });

    }

    return retObj
}

Vue.filter('fromNow', function (value) {
    return moment(value).fromNow();
})

Vue.filter('formatBytes', function (value) {
    var bytes = parseFloat(value);
    if (bytes < 0) return "-";
    else if (bytes < 1024) return bytes + " B";
    else if (bytes < 1048576) return (bytes / 1024).toFixed(0) + " KB";
    else if (bytes < 1073741824) return (bytes / 1048576).toFixed(1) + " MB";
    else return (bytes / 1073741824).toFixed(1) + " GB";
})

$(function () {
    $.scrollUp({
        scrollText: '', // text are defined in css
    });

    // For page first loading
    loadFileList(location.pathname + location.search)

    // update version
    $.getJSON("/-/sysinfo", function (res) {
        vm.version = res.version;
    })

    var clipboard = new Clipboard('.btn');
    clipboard.on('success', function (e) {
        console.info('Action:', e.action);
        console.info('Text:', e.text);
        console.info('Trigger:', e.trigger);
        $(e.trigger)
            .tooltip('show')
            .mouseleave(function () {
                $(this).tooltip('hide');
            })

        e.clearSelection();
    });
});
