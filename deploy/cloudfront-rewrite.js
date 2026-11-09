// CloudFront viewer-request function "perfuse-rewrite" for perfuse.health (distribution E1EQTICSGCWXC9).
// Every page has one address, https://perfuse.health/PATH/, so search engines see no duplicates:
// www.perfuse.health, /PATH without the slash and /PATH/index.html answer 301 to it.
// Publish: aws cloudfront update-function (this file), test-function, then publish-function.
function handler(event) {
    var request = event.request;
    var uri = request.uri;
    var host = request.headers.host ? request.headers.host.value : '';

    var target = uri;
    if (target.endsWith('/index.html')) {
        target = target.substring(0, target.length - 'index.html'.length);
    } else if (!target.endsWith('/')) {
        // A path with no file extension is a page, not an asset.
        var last = target.substring(target.lastIndexOf('/') + 1);
        if (last.indexOf('.') === -1) {
            target = target + '/';
        }
    }

    if (target !== uri || host === 'www.perfuse.health') {
        var query = [];
        for (var key in request.querystring) {
            var q = request.querystring[key];
            var values = q.multiValue ? q.multiValue : [q];
            for (var i = 0; i < values.length; i++) {
                query.push(values[i].value === '' ? key : key + '=' + values[i].value);
            }
        }
        var location = 'https://perfuse.health' + target + (query.length ? '?' + query.join('&') : '');
        return {
            statusCode: 301,
            statusDescription: 'Moved Permanently',
            headers: {
                location: { value: location },
                'cache-control': { value: 'public, max-age=86400' }
            }
        };
    }

    // S3 behind an origin access control serves objects by exact key and has no default document
    // below the root, so a directory URL needs index.html appended.
    if (uri.endsWith('/')) {
        request.uri = uri + 'index.html';
    }
    return request;
}
