function FileMultipartUploadApi(config) {

    this.host = "//" + (config.host || 'localhost:8000')
    this.filePath = config.filePath || ''
    this.uploadId = ''

    var _updateUrl = (function () {
        return this.host + this.filePath
    }).bind(this)

    this.initUpload = (function (option) {
        // return {
        //     Bucket: 'bucketName',
        //     Key: 'example-object',
        //     UploadId: 'xxxxxx'
        // }
        option = option || {}
        var url = _updateUrl() + '?uploads'
        return $.ajax({
            url: url,
            type: 'POST',
            async: option.async === undefined ? true : option.async,
            success: function (data) {
                option.success && option.success(data)
            },
            error: function (msg) {
                console.error(msg)
            }
        })
    }).bind(this)
    this.uploadPart = (function (option, ajaxObj) {
        // return {
        //     code:200
        //     msg:OK
        // }
        option = option || {}
        var dtd = $.Deferred()
        var url = _updateUrl()
        url += '?partNumber=' + option.partNumber
        url += '&uploadId=' + this.uploadId
        ajaxObj.obj = $.ajax({
            url: url,
            type: 'PUT',
            data: option.data,
            async: option.async === undefined ? true : option.async,
            cache: false,
            contentType: false,
            processData: false,
            headers: {
                "Accept": "*/*",
                "Cache-Control": "no-cache",
                "X-Requested-With": "XMLHttpRequest"
            },
            xhr: function () {
                var xhr = $.ajaxSettings.xhr()
                xhr.upload.onprogress = function (evt) {
                    option.onprogress(evt)
                }
                return xhr
            }
        })
        ajaxObj.obj.then(function (data, status, xhr) {
            var respHeaders = xhr.getAllResponseHeaders()
            var headerMap = {};
            respHeaders.trim().split(/[\r\n]+/).forEach(function (line) {
                var parts = line.split(': ');
                var header = parts.shift();
                headerMap[header] = parts.join(': ');
            });
            dtd.resolve.apply(undefined, [data, headerMap, status, xhr])
        }, function (msg) {
            console.error(msg)
        })
        return dtd
    }).bind(this)
    this.completeMultipartUpload = (function (option) {
        //url='/${example-object}?uploadId=${uploadId}'
        //data=[
        //     {
        //          partNumber:1,
        //          ETag:'xxx',
        //      },
        //     {
        //          partNumber:2,
        //          ETag:'xxx',
        //      },
        // ]
        // return {
        //     location:http://${bucketName}.s3.netease.com/${Example-Object}
        //     bucket:${bucketName}
        //     key:${Example-Object}
        //     ETag:'xxx'
        // }
        option = option || {}
        var url = _updateUrl()
        url += '?uploadId=' + this.uploadId
        return $.ajax({
            url: url,
            type: 'POST',
            data: option.data,
            async: option.async === undefined ? true : option.async,
            error: function (msg, status) {
                console.error(msg, status)
            }
        })
    }).bind(this)
    this.abortMultipartUpload = (function (option) {
        //url='/${example-object}?uploadId=${uploadId}'
        // return {
        //     code:204
        //     msg:OK
        // }
        option = option || {}
        var url = _updateUrl()
        url += '?uploadId=' + this.uploadId
        return $.ajax({
            url: url,
            type: 'DELETE',
            async: option.async === undefined ? true : option.async,
            error: function (res) {
                console.error(res)
            }
        })
    }).bind(this)

    this.getParamFromXml = function (xml, paramName) {
        return xml.getElementsByTagName(paramName)[0].textContent
    }
}


