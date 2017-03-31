package device

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Comcast/webpa-common/httperror"
	"github.com/Comcast/webpa-common/logging"
	"github.com/Comcast/webpa-common/wrp"
	"io"
	"io/ioutil"
	"net/http"
	"sync"
	"time"
)

// MessageHandler is a configurable http.Handler which handles inbound WRP traffic
// to be sent to devices.
type MessageHandler struct {
	// Logger is the sink for logging output.  If not set, logging will be sent to logging.DefaultLogger().
	Logger logging.Logger

	// RequestDecoders is the pool of wrp.Decoder objects used to decode http.Request bodies
	// sent to this handler.  This field is required.
	RequestDecoders *wrp.DecoderPool

	// ResponseEncoders is the pool of wrp.Encoder objects used to encode wrp messages sent
	// as HTTP responses.  This field is required.
	ResponseEncoders *wrp.EncoderPool

	// DeviceEncoders is the optional pool of wrp.Encoder objects used to transcode messages
	// into the format accepted by devices, which is normally wrp.Msgpack.  If this field is not
	// sent, then the HTTP request body is assumed to be valid for on-the-wire transport to devices.
	DeviceEncoders *wrp.EncoderPool

	// Router is the device message Router to use.  This field is required.
	Router Router

	// Timeout is the optional timeout for all operations through this handler
	Timeout time.Duration
}

func (mh *MessageHandler) createContext(request *http.Request) (ctx context.Context, cancel context.CancelFunc) {
	ctx = request.Context()
	if mh.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, mh.Timeout)
	}

	return
}

func (mh *MessageHandler) decodeRequest(request *http.Request) (message wrp.Routable, body []byte, err error) {
	if body, err = ioutil.ReadAll(request.Body); err != nil {
		return
	}

	message = new(wrp.Message)
	err = mh.RequestDecoders.DecodeBytes(message, body)
	return
}

func (mh *MessageHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	logger := mh.Logger
	if logger == nil {
		logger = logging.DefaultLogger()
	}

	ctx, cancel := mh.createContext(request)
	if cancel != nil {
		defer cancel()
	}

	message, contents, err := mh.decodeRequest(request)
	if err != nil {
		httperror.Formatf(
			response,
			http.StatusBadRequest,
			"Could not decode WRP message: %s",
			err,
		)

		return
	}

	if mh.DeviceEncoders != nil {
		contents = contents[:0]
		if err := mh.DeviceEncoders.EncodeBytes(&contents, message); err != nil {
			httperror.Formatf(
				response,
				http.StatusInternalServerError,
				"Could not encode WRP message: %s",
				err,
			)

			return
		}
	}

	deviceRequest, err := NewRequest(message, contents, ctx)
	if err != nil {
		httperror.Formatf(
			response,
			http.StatusBadRequest,
			"Could not create device request: %s",
			err,
		)

		return
	}

	if deviceResponse, err := mh.Router.Route(deviceRequest); err != nil {
		code := http.StatusInternalServerError
		if err == ErrorDeviceNotFound {
			code = http.StatusNotFound
		}

		httperror.Formatf(
			response,
			code,
			"Could not process device request: %s",
			err,
		)
	} else if deviceResponse != nil {
		if deviceResponse.Error != nil {
			httperror.Formatf(
				response,
				http.StatusInternalServerError,
				"Device transaction failed: %s",
				err,
			)
		} else if mh.ResponseEncoders != nil {
			response.Header().Set("Content-Type", mh.ResponseEncoders.Format().ContentType())
			if err := mh.ResponseEncoders.Encode(response, deviceResponse); err != nil {
				logger.Error("Error while encoding WRP response: %s", err)
			}
		} else {
			response.Header().Set("Content-Type", wrp.Msgpack.ContentType())
			if _, err := response.Write(deviceResponse.Contents); err != nil {
				logger.Error("Error while writing response contents: %s", err)
			}
		}
	}
}

type ConnectHandler struct {
	Logger         logging.Logger
	Connector      Connector
	ResponseHeader http.Header
}

func (ch *ConnectHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	logger := ch.Logger
	if logger == nil {
		logger = logging.DefaultLogger()
	}

	device, err := ch.Connector.Connect(response, request, ch.ResponseHeader)
	if err != nil {
		logger.Error("Failed to connect device: %s", err)
	} else {
		logger.Debug("Connected device: %s", device.ID())
	}
}

// NewDeviceListHandler returns an http.Handler that renders a JSON listing
// of the devices within a manager.
func NewDeviceListHandler(manager Manager, logger logging.Logger) http.Handler {
	if logger == nil {
		logger = logging.DefaultLogger()
	}

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		flusher := response.(http.Flusher)
		response.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(response, `{"device": [`); err != nil {
			logger.Error("Unable to write content: %s", err)
			return
		}

		devices := make(chan Interface, 100)
		finish := new(sync.WaitGroup)
		finish.Add(1)

		// to minimize the time we hold the read lock on the Manager, spawn a goroutine
		// that collects devices and inserts them into an output buffer
		go func() {
			defer finish.Done()

			needsDelimiter := false
			for d := range devices {
				if needsDelimiter {
					io.WriteString(response, ",")
				}

				needsDelimiter = true
				if data, err := json.Marshal(d); err != nil {
					message := fmt.Sprintf("Unable to marshal device [%s] as JSON: %s", d.ID(), err)
					logger.Error(message)
					fmt.Fprintf(response, `"%s"`, message)
				} else {
					response.Write(data)
				}

				flusher.Flush()
			}
		}()

		manager.VisitAll(func(d Interface) {
			devices <- d
		})

		close(devices)
		finish.Wait()
		io.WriteString(response, `]}`)
		flusher.Flush()
	})
}
