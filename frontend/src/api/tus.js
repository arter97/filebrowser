import * as tus from "tus-js-client";
import { tusEndpoint } from "@/utils/constants";
import store from "@/store";
import { removePrefix } from "./utils";

// Make following configurable by envs?
export const chunkSize = 20 * 1000 * 1000;
const parallelUploads = 3;

export async function upload(url, content = "", overwrite = false, onupload) {
  return new Promise((resolve, reject) => {
    var upload = new tus.Upload(content, {
      httpStack: new CustomHTTPStack(),
      endpoint: tusEndpoint,
      chunkSize: chunkSize,
      retryDelays: [0, 3000, 5000, 10000, 20000],
      parallelUploads: parallelUploads,
      metadata: {
        filename: content.name,
        filetype: content.type,
        overwrite: overwrite,
        destination: removePrefix(url),
      },
      headers: {
        "X-Auth": store.state.jwt,
      },
      onError: function (error) {
        reject("Upload failed: " + error);
      },
      onProgress: function (bytesUploaded) {
        // Emulate ProgressEvent.loaded which is used by calling functions
        // loaded is specified in bytes (https://developer.mozilla.org/en-US/docs/Web/API/ProgressEvent/loaded)
        if (typeof onupload === "function") {
          onupload({ loaded: bytesUploaded });
        }
      },
      onSuccess: function () {
        resolve();
      },
    });

    upload.findPreviousUploads().then(function (previousUploads) {
      if (previousUploads.length) {
        upload.resumeFromPreviousUpload(previousUploads[0]);
      }
      upload.start();
    });
  });
}

/* eslint-disable max-classes-per-file */
// Tus sends a location header with the first POST request. In proxied environments, this header is not correct.
// The documentation suggests to change proxy config (https://github.com/tus/tusd/blob/master/docs/faq.md#can-i-run-tusd-behind-a-reverse-proxy)
// However, this needs user config and is not always possible (e.g. served at a certain sub-path)
// A custom http stack overwriting the responses' location header is the better solution here as we know the correct and fixed location
class CustomHTTPStack {
  createRequest(method, url) {
    return new Request(method, url);
  }

  getName() {
    return "CustomHTTPStack";
  }
}

class Request {
  constructor(method, url) {
    this._xhr = new XMLHttpRequest();
    this._xhr.open(method, url, true);

    this._method = method;
    this._url = url;
    this._headers = {};
  }

  getMethod() {
    return this._method;
  }

  getURL() {
    return this._url;
  }

  setHeader(header, value) {
    this._xhr.setRequestHeader(header, value);
    this._headers[header] = value;
  }

  getHeader(header) {
    return this._headers[header];
  }

  setProgressHandler(progressHandler) {
    // Test support for progress events before attaching an event listener
    if (!("upload" in this._xhr)) {
      return;
    }

    this._xhr.upload.onprogress = (e) => {
      if (!e.lengthComputable) {
        return;
      }

      progressHandler(e.loaded);
    };
  }

  send(body = null) {
    return new Promise((resolve, reject) => {
      this._xhr.onload = () => {
        resolve(new Response(this._xhr));
      };

      this._xhr.onerror = (err) => {
        reject(err);
      };

      this._xhr.send(body);
    });
  }

  abort() {
    this._xhr.abort();
    return Promise.resolve();
  }

  getUnderlyingObject() {
    return this._xhr;
  }
}

class Response {
  constructor(xhr) {
    this._xhr = xhr;
  }

  getStatus() {
    return this._xhr.status;
  }

  getHeader(header) {
    const result = this._xhr.getResponseHeader(header);
    if ("location" === header.toLowerCase()) {
      // Fix location header
      return tusEndpoint + "/" + result.replace(/^.*[\\/]/, "");
    }
    return result;
  }

  getBody() {
    return this._xhr.responseText;
  }

  getUnderlyingObject() {
    return this._xhr;
  }
}
