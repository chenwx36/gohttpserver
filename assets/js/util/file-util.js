/**
 * FileUtil 工具类
 * @constructor
 */
function FileUtil() {

    /**
     * 将文件按chunkSize切块，返回切块后的Blob[]
     * @param fileObj
     * @param chunkSizeByte
     * @returns {Array}
     */
    this.sliceFile = function (fileObj, chunkSizeByte) {
        var chunks = []
        var fileLength = fileObj.size
        if (chunkSizeByte >= fileLength) return [{
            fileBlob: blobToFile(fileObj.slice(0)),
            length: fileLength,
            loaded: 0
        }]
        var start = 0, end = chunkSizeByte
        while (end < fileLength) {
            chunks.push({
                fileBlob: blobToFile(fileObj.slice(start, end)),
                length: chunkSizeByte,
                loaded: 0
            })
            start = end
            end += chunkSizeByte
            if (end >= fileLength) {
                chunks.push({
                    fileBlob: blobToFile(fileObj.slice(start, fileLength)),
                    length: fileLength - start,
                    loaded: 0
                })
                break
            }
        }
        return chunks
    }

}

function blobToFile(blob) {
    // return new File([blob], Math.random().toString())
    return blob
}


/**
 * 将文件块结合otherParams构建formDataList
 * @param fileChunks
 * @param otherParams
 * @returns Array
 */
function getFormDataByFileChunks(fileChunks, otherParams) {
    return fileChunks.map(function (chunk) {
        var formData = new FormData()
        formData.append('file', chunk)
        Object.keys(otherParams).forEach(function (key) {
            formData.append(key, otherParams[key])
        })
        return formData
    })
}


/**
 * 通过FormData进行上传
 * @param option
 */
function ajaxByFormData(option) {
    $.ajax({
        url: option.url,
        type: 'POST',
        data: option.data,
        async: option.async === undefined ? true : option.async,
        cache: false,
        timeout: 0,  // wait forever
        contentType: false,
        processData: false,
        success: function (data) {
            option.success(data)
        },
        error: function (res) {
            option.error(res)
        }
    });
}



