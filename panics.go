package panics

import (
	"bytes"
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
	"syscall"
	"time"

	"strings"

	"github.com/julienschmidt/httprouter"
	"github.com/nsqio/go-nsq"
)

var (
	env             string
	filepath        string
	slackWebhookURL string
	slackChannel    string
	tagString       string

	capturedBadDeployment bool
	customMessage         string
)

type Tags map[string]string

type Options struct {
	Env             string
	Filepath        string
	SentryDSN       string
	SlackWebhookURL string
	SlackChannel    string
	Tags            Tags
	CustomMessage   string
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

	CaptureBadDeployment()
}

func init() {
	env = os.Getenv("TKPENV")
}

// CaptureHandler handle panic on http handler.
func CaptureHandler(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		request, _ := httputil.DumpRequest(r, true)
		defer func() {
			r := recover()

			if r != nil {
				switch t := r.(type) {
				case string:
					err = errors.New(t)
				case error:
					err = t
				default:
					err = errors.New("Unknown error")
				}

				publishError(err, request, true)

				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}()
		h.ServeHTTP(w, r)
	}
}

// CaptureHTTPRouterHandler handle panic on httprouter handler.
func CaptureHTTPRouterHandler(h httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		var err error
		request, _ := httputil.DumpRequest(r, true)
		defer func() {
			r := recover()

			if r != nil {
				switch t := r.(type) {
				case string:
					err = errors.New(t)
				case error:
					err = t
				default:
					err = errors.New("Unknown error")
				}

				publishError(err, request, true)

				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}()
		h(w, r, ps)
	}
}

// CaptureNegroniHandler handle panic on negroni handler.
func CaptureNegroniHandler(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	var err error
	request, _ := httputil.DumpRequest(r, true)
	defer func() {
		r := recover()

		if r != nil {
			switch t := r.(type) {
			case string:
				err = errors.New(t)
			case error:
				err = t
			default:
				err = errors.New("Unknown error")
			}

			publishError(err, request, true)

			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}()
	next(w, r)
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

func CaptureNSQConsumer(handler nsq.HandlerFunc) nsq.HandlerFunc {
	return func(message *nsq.Message) error {
		var err error
		defer func() {
			r := recover()

			if r != nil {
				switch t := r.(type) {
				case string:
					err = errors.New(t)
				case error:
					err = t
				default:
					err = errors.New("Unknown error")
				}

				publishError(err, nil, true)
			}
		}()
		return handler(message)
	}
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
