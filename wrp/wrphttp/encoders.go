package wrphttp

import (
	"bytes"
	"context"
	"io/ioutil"
	"net/http"

	"github.com/Comcast/webpa-common/tracing/tracinghttp"
	"github.com/Comcast/webpa-common/wrp"
	"github.com/Comcast/webpa-common/wrp/wrpendpoint"
	gokithttp "github.com/go-kit/kit/transport/http"
)

// EncodeRequest returns a go-kit EncodeRequestFunc that encodes a decoded Entity as an HTTP request,
// often as the component of a fanout (though not required).  The given WRP format is used as the HTTP entity format.
func EncodeRequest(format wrp.Format) gokithttp.EncodeRequestFunc {
	return func(ctx context.Context, component *http.Request, v interface{}) error {
		entity := v.(*Entity)

		if format == entity.Format && len(entity.Contents) > 0 {
			// the entity is already formatted properly, so just write its contents out
			component.Body = ioutil.NopCloser(bytes.NewReader(entity.Contents))
			component.ContentLength = int64(len(entity.Contents))
		} else {
			var transcoded []byte
			if err := wrp.NewEncoderBytes(&transcoded, format).Encode(&entity.Message); err != nil {
				return err
			}

			component.Body = ioutil.NopCloser(bytes.NewReader(transcoded))
			component.ContentLength = int64(len(transcoded))
		}

		component.Header.Set("Content-Type", format.ContentType())
		component.Header.Set(DestinationHeader, entity.Message.Destination)
		return nil
	}
}

// ClientEncodeRequestBody produces a go-kit transport/http.EncodeRequestFunc for use when sending WRP requests
// to HTTP clients.  The returned decoder will set the appropriate headers and set the body to the encoded
// WRP message in the request.
func ClientEncodeRequestBody(format wrp.Format, custom http.Header) gokithttp.EncodeRequestFunc {
	return func(ctx context.Context, httpRequest *http.Request, value interface{}) error {
		var (
			wrpRequest = value.(wrpendpoint.Request)
			body       = new(bytes.Buffer)
		)

		if err := wrpRequest.Encode(body, format); err != nil {
			return err
		}

		for name, values := range custom {
			for _, value := range values {
				httpRequest.Header.Add(name, value)
			}
		}

		httpRequest.Header.Set(DestinationHeader, wrpRequest.Destination())
		httpRequest.Header.Set("Content-Type", format.ContentType())
		httpRequest.ContentLength = int64(body.Len())
		httpRequest.Body = ioutil.NopCloser(body)
		return nil
	}
}

// ClientEncodeRequestHeaders is a go-kit transport/http.EncodeRequestFunc for use when sending WRP requests
// to HTTP clients using an HTTP header representation of the message fields.
func ClientEncodeRequestHeaders(custom http.Header) gokithttp.EncodeRequestFunc {
	return func(ctx context.Context, httpRequest *http.Request, value interface{}) error {
		var (
			wrpRequest = value.(wrpendpoint.Request)
			body       = new(bytes.Buffer)
		)

		if err := WriteMessagePayload(httpRequest.Header, body, wrpRequest.Message()); err != nil {
			return err
		}

		for name, values := range custom {
			for _, value := range values {
				httpRequest.Header.Add(name, value)
			}
		}

		AddMessageHeaders(httpRequest.Header, wrpRequest.Message())
		httpRequest.ContentLength = int64(body.Len())
		httpRequest.Body = ioutil.NopCloser(body)

		return nil
	}
}

// ServerEncodeResponseBody produces a go-kit transport/http.EncodeResponseFunc that transforms a wrphttp.Response into
// an HTTP response.
func ServerEncodeResponseBody(timeLayout string, format wrp.Format) gokithttp.EncodeResponseFunc {
	return func(ctx context.Context, httpResponse http.ResponseWriter, value interface{}) error {
		var (
			wrpResponse = value.(wrpendpoint.Response)
			output      bytes.Buffer
		)

		tracinghttp.HeadersForSpans(wrpResponse.Spans(), timeLayout, httpResponse.Header())

		if err := wrpResponse.Encode(&output, format); err != nil {
			return err
		}

		httpResponse.Header().Set("Content-Type", format.ContentType())
		_, err := output.WriteTo(httpResponse)
		return err
	}
}

// ServerEncodeResponseHeaders encodes a WRP response's fields into the HTTP response's headers.  The payload
// is written as the HTTP response body.
func ServerEncodeResponseHeaders(timeLayout string) gokithttp.EncodeResponseFunc {
	return func(ctx context.Context, httpResponse http.ResponseWriter, value interface{}) error {
		wrpResponse := value.(wrpendpoint.Response)
		tracinghttp.HeadersForSpans(wrpResponse.Spans(), timeLayout, httpResponse.Header())
		AddMessageHeaders(httpResponse.Header(), wrpResponse.Message())
		return WriteMessagePayload(httpResponse.Header(), httpResponse, wrpResponse.Message())
	}
}
