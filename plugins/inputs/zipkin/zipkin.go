package zipkin

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
)

const (
	// DefaultPort is the default port zipkin listens on, which zipkin implementations
	// expect.
	DefaultPort = 9411

	// DefaultRoute is the default route zipkin uses, and zipkin implementations
	// expect.
	DefaultRoute = "/api/v1/spans"

	// DefaultShutdownTimeout is the max amount of time telegraf will wait
	// for the plugin to shutdown
	DefaultShutdownTimeout = 5
)

// Recorder represents a type which can record zipkin trace data as well as
// any accompanying errors, and process that data.
type Recorder interface {
	Record(Trace) error
	Error(error)
}

// Handler represents a type which can register itself with a router for
// http routing, and a Recorder for trace data collection.
type Handler interface {
	Register(router *mux.Router, recorder Recorder) error
}

// BinaryAnnotation represents a zipkin binary annotation. It contains
// all of the same fields as might be found in its zipkin counterpart.
type BinaryAnnotation struct {
	Key         string
	Value       string
	Host        string // annotation.endpoint.ipv4 + ":" + annotation.endpoint.port
	ServiceName string
	Type        string
}

// Annotation represents an ordinary zipkin annotation. It contains the data fields
// which will become fields/tags in influxdb
type Annotation struct {
	Timestamp   time.Time
	Value       string
	Host        string // annotation.endpoint.ipv4 + ":" + annotation.endpoint.port
	ServiceName string
}

//Span represents a specific zipkin span. It holds the majority of the same
// data as a zipkin span sent via the thrift protocol, but is presented in a
// format which is more straightforward for storage purposes.
type Span struct {
	ID                string
	TraceID           string // zipkin traceid high concat with traceid
	Name              string
	ParentID          string
	Timestamp         time.Time // If zipkin input is nil then time.Now()
	Duration          time.Duration
	Annotations       []Annotation
	BinaryAnnotations []BinaryAnnotation
}

// Trace is an array (or a series) of spans
type Trace []Span

const sampleConfig = `
  ##
  # path = /path/your/zipkin/impl/posts/to
  # port = <port your service posts to>
`

// Zipkin is a telegraf configuration structure for the zipkin input plugin,
// but it also contains fields for the management of a separate, concurrent
// zipkin http server
type Zipkin struct {
	ServiceAddress string
	Port           int
	Path           string
	handler        Handler
	server         *http.Server
	waitGroup      *sync.WaitGroup
}

// Description is a necessary method implementation from telegraf.ServiceInput
func (z Zipkin) Description() string {
	return "Allows for the collection of zipkin tracing spans for storage in InfluxDB"
}

// SampleConfig is a  necessary  method implementation from telegraf.ServiceInput
func (z Zipkin) SampleConfig() string {
	return sampleConfig
}

// Gather is empty for the zipkin plugin; all gathering is done through
// the separate goroutine launched in (*Zipkin).Start()
func (z *Zipkin) Gather(acc telegraf.Accumulator) error { return nil }

// Start launches a separate goroutine for collecting zipkin client http requests,
// passing in a telegraf.Accumulator such that data can be collected.
func (z *Zipkin) Start(acc telegraf.Accumulator) error {
	log.Println("starting...")
	if z.handler == nil {
		z.handler = NewSpanHandler(z.Path)
	}

	var wg sync.WaitGroup
	z.waitGroup = &wg

	go func() {
		wg.Add(1)
		defer wg.Done()

		z.Listen(acc)
	}()

	return nil
}

// Stop shuts the internal http server down with via context.Context
func (z *Zipkin) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultShutdownTimeout)
	defer cancel()

	defer z.waitGroup.Wait()
	z.server.Shutdown(ctx)
}

// Listen creates an http server on the zipkin instance it is called with, and
// serves http until it is stopped by Zipkin's (*Zipkin).Stop()  method.
func (z *Zipkin) Listen(acc telegraf.Accumulator) {
	router := mux.NewRouter()
	converter := NewLineProtocolConverter(acc)
	z.handler.Register(router, converter)

	if z.server == nil {
		z.server = &http.Server{
			Addr:    ":" + strconv.Itoa(z.Port),
			Handler: router,
		}
	}
	if err := z.server.ListenAndServe(); err != nil {
		acc.AddError(fmt.Errorf("E! Error listening: %v", err))
	}
}

func init() {
	inputs.Add("zipkin", func() telegraf.Input {
		return &Zipkin{
			Path: DefaultRoute,
			Port: DefaultPort,
		}
	})
}
