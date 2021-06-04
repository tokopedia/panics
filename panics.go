package panics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/eapache/go-resiliency/breaker"
	"github.com/gin-gonic/gin"
	"github.com/julienschmidt/httprouter"
	"github.com/nsqio/go-nsq"
	"google.golang.org/grpc"
)

var (
	env                   string
	filepath              string
	slackWebhookURL       string
	slackChannel          string
	tagString             string
	capturedBadDeployment bool
	customMessage         string
	// circuit breaker
	cb *breaker.Breaker
	// ErrorPanic variable used as global error message
	ErrorPanic = errors.New("Panic happened")

	datadogClient DatadogClient
	serviceName   string
	// use specific tags env and serviceName when adding monitoring datadog when panic occured
	datadogMetricName = "panic.capture_panic"
)

// DatadogClient is the interface of the datadog client used by panic lib
type DatadogClient interface {
	Count(name string, value int64, tags []string, rate float64) error
}

type Tags map[string]string

type Options struct {
	Env             string
	Filepath        string
	SentryDSN       string
	SlackWebhookURL string
	SlackChannel    string
	Tags            Tags
	CustomMessage   string
	DontLetMeDie    bool
	DatadogClient   DatadogClient
	ServiceName     string
}

func SetOptions(o *Options) {
	filepath = o.Filepath
	slackWebhookURL = o.SlackWebhookURL
	slackChannel = o.SlackChannel

	env = o.Env

	var tmp []string
	for key, val := range o.Tags {
		tmp = append(tmp, fmt.Sprintf("`%s: %s`", key, val))
	}
	tagString = strings.Join(tmp, " | ")

	customMessage = o.CustomMessage

	datadogClient, serviceName = o.DatadogClient, o.ServiceName

	// set circuit breaker to nil
	if o.DontLetMeDie {
		cb = nil
	}
	CaptureBadDeployment()
}

func init() {
	env = os.Getenv("TKPENV")
	// circuitbreaker to let apps died when got too many panics
	cb = breaker.New(3, 2, time.Minute*1)
}

// CaptureHandler handle panic on http handler.
func CaptureHandler(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		request, _ := httputil.DumpRequest(r, true)
		defer func() {
			if !recoveryBreak() {
				r := panicRecover(recover())
				if r != nil {
					publishError(r, request, true)
					http.Error(w, r.Error(), http.StatusInternalServerError)
				}
			}
		}()
		h.ServeHTTP(w, r)
	}
}

// CaptureHTTPRouterHandler handle panic on httprouter handler.
func CaptureHTTPRouterHandler(h httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		request, _ := httputil.DumpRequest(r, true)
		defer func() {
			if !recoveryBreak() {
				r := panicRecover(recover())
				if r != nil {
					publishError(r, request, true)
					http.Error(w, r.Error(), http.StatusInternalServerError)
				}
			}
		}()
		h(w, r, ps)
	}
}

// CaptureNegroniHandler handle panic on negroni handler.
func CaptureNegroniHandler(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	request, _ := httputil.DumpRequest(r, true)
	defer func() {
		if !recoveryBreak() {
			r := panicRecover(recover())
			if r != nil {
				publishError(r, request, true)
				http.Error(w, r.Error(), http.StatusInternalServerError)
			}
		}
	}()
	next(w, r)
}

// CaptureGinHandler handle panic on gin handler.
func CaptureGinHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		request, _ := httputil.DumpRequest(c.Request, true)
		defer func() {
			if !recoveryBreak() {
				r := panicRecover(recover())
				if r != nil {
					publishError(r, request, true)
					http.Error(c.Writer, r.Error(), http.StatusInternalServerError)
				}
			}
		}()

		c.Next()
	}
}

// Capture will publish any errors
func Capture(err string, message ...string) {
	var tmp string
	for i, val := range message {
		if i == 0 {
			tmp += val
		} else {
			tmp += fmt.Sprintf("\n\n%s", val)
		}
	}

	publishError(errors.New(err), []byte(tmp), false)
}

// Capture will publish any errors with stack trace
func CaptureWithStackTrace(err string, message ...string) {
	var tmp string
	for i, val := range message {
		if i == 0 {
			tmp += val
		} else {
			tmp += fmt.Sprintf("\n\n%s", val)
		}
	}

	publishError(errors.New(err), []byte(tmp), true)
}

// CaptureBadDeployment will listen to SIGCHLD signal, and send notification when it's receive one.
func CaptureBadDeployment() {
	if !capturedBadDeployment {
		capturedBadDeployment = true
		go func() {
			term := make(chan os.Signal)
			signal.Notify(term, syscall.SIGUSR1)
			for {
				select {
				case <-term:
					publishError(errors.New("Failed to deploy an application"), nil, false)
				}
			}
		}()
	}
}

// CaptureNSQConsumer capture panics on NSQ consumer
func CaptureNSQConsumer(handler nsq.HandlerFunc) nsq.HandlerFunc {
	return func(message *nsq.Message) error {
		defer func() {
			r := panicRecover(recover())
			if r != nil {
				publishError(r, nil, true)
			}
		}()
		return handler(message)
	}
}

// CaptureGoroutine wrap function call with goroutines and send notification when there's panic inside it
//
// Receives handle function that will be executed on normal condition and recovery function that will be executed in-case there's panic
func CaptureGoroutine(handleFn func(), recoveryFn func()) {
	defer func() {
		if !recoveryBreak() {
			rcv := panicRecover(recover())
			if rcv != nil {
				fmt.Fprintf(os.Stderr, "Panic: %+v\n", rcv)
				debug.PrintStack()
				publishError(rcv, nil, true)
				recoveryFn()
			}
		}
	}()
	handleFn()
}

// HTTPRecoveryMiddleware act as middleware that capture panics standard in http handler
func HTTPRecoveryMiddleware(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		request, _ := httputil.DumpRequest(r, true)
		defer func() {
			if !recoveryBreak() {
				rcv := panicRecover(recover())
				if rcv != nil {
					// log the panic
					fmt.Fprintf(os.Stderr, "Panic: %+v\n", rcv)
					debug.PrintStack()
					publishError(rcv, request, true)
					http.Error(w, rcv.Error(), http.StatusInternalServerError)
				}
			}
		}()

		next.ServeHTTP(w, r)
	}

	return http.HandlerFunc(fn)
}

// UnaryServerInterceptor intercept the execution of a unary RPC on the server when panic happen
func UnaryServerInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	defer func() {
		if !recoveryBreak() {
			rcv := panicRecover(recover())
			if rcv != nil {
				fmt.Fprintf(os.Stderr, "Panic: %+v\n", rcv)
				debug.PrintStack()
				publishError(rcv, nil, true)
			}
		}
	}()
	return handler(ctx, req)
}

func panicRecover(rc interface{}) error {
	if cb != nil {
		r := cb.Run(func() error {
			return recovery(rc)
		})
		return r
	}
	return recovery(rc)
}

func recoveryBreak() bool {
	if cb == nil {
		return false
	}

	if err := cb.Run(func() error {
		return nil
	}); err == breaker.ErrBreakerOpen {
		return true
	}
	return false
}

func recovery(r interface{}) error {
	var err error
	if r != nil {
		switch t := r.(type) {
		case string:
			err = errors.New(t)
		case error:
			err = t
		default:
			err = errors.New("Unknown error")
		}
	}
	return err
}

func publishError(errs error, reqBody []byte, withStackTrace bool) {
	var text string
	var snip string
	var buffer bytes.Buffer
	errorStack := debug.Stack()
	buffer.WriteString(fmt.Sprintf(`[%s] *%s*`, env, errs.Error()))

	if len(tagString) > 0 {
		buffer.WriteString(" | " + tagString)
	}

	if customMessage != "" {
		buffer.WriteString("\n" + customMessage + "\n")
	}

	if reqBody != nil {
		buffer.WriteString(fmt.Sprintf(" ```%s``` ", string(reqBody)))
	}
	text = buffer.String()

	if errorStack != nil && withStackTrace {
		snip = fmt.Sprintf("```\n%s```", string(errorStack))
	}

	if slackWebhookURL != "" {
		go postToSlack(buffer.String(), snip)
	}
	if filepath != "" {
		go func() {
			fp := fmt.Sprintf("%s/panics.log", filepath)
			file, err := os.OpenFile(fp, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
			if err != nil {
				log.Printf("[panics] failed to open file %s", fp)
				return
			}
			file.Write([]byte(text))
			file.Write([]byte(snip + "\r\n"))
			file.Close()
		}()
	}
	if datadogClient != nil {
		tags := []string{fmt.Sprintf("service:%s", serviceName), fmt.Sprintf("env:%s", env)}
		go datadogClient.Count(datadogMetricName, 1, tags, 1)
	}
}

func postToSlack(text, snip string) {
	payload := map[string]interface{}{
		"text": text,
		//Enable slack to parse mention @<someone>
		"link_names": 1,
		"attachments": []map[string]interface{}{
			map[string]interface{}{
				"text":      snip,
				"color":     "#e50606",
				"title":     "Stack Trace",
				"mrkdwn_in": []string{"text"},
			},
		},
	}
	if slackChannel != "" {
		payload["channel"] = slackChannel
	}
	b, err := json.Marshal(payload)
	if err != nil {
		log.Println("[panics] marshal err", err, text, snip)
		return
	}

	client := http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Post(slackWebhookURL, "application/json", bytes.NewBuffer(b))
	if err != nil {
		log.Printf("[panics] error on capturing error : %s %s %s\n", err.Error(), text, snip)
		return
	}

	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Printf("[panics] error on capturing error : %s %s %s\n", err, text, snip)
			return
		}
		log.Printf("[panics] error on capturing error : %s %s %s\n", string(b), text, snip)
	}
}
