// Copyright (c) 2015 Pani Networks
// All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

// This file contains things related to the REST framework.
package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/K-Phoen/negotiation"
	"github.com/codegangsta/negroni"
	"github.com/gorilla/context"
	"github.com/gorilla/mux"
	"github.com/mitchellh/mapstructure"
	"io/ioutil"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

// RestHandler specifies type of a function that each Route provides.
// It takes (for now) an interface as input, and returns any
// interface. The middleware provided in this file takes care
// of unmarshalling the data from the wire to the input object
// (the type of the object created will be determined by the
// type of the instance provided in Consumes field of Route type, below),
// and of marshalling the returned object to the wire (the type of
// which is determined by type of the instance provided in Produces
// field of Route type, below).
type RestHandler func(input interface{}) (interface{}, error)

// UnwrappedRestHandlerInput is used to pass in
// http.Request and http.ResponseWriter, should some
// service like unfettered access directly to them. In
// such a case, the service's RestHandler's input will be of this type;
// and the return value will be ignored.
type UnwrappedRestHandlerInput struct {
	ResponseWriter http.ResponseWriter
	Request        *http.Request
}

// Route determines an action taken on a URL pattern/HTTP method.
// Each service can define a route
// See routes.go and handlers.go in root package for a demonstration
// of use
type Route struct {
	// REST method
	Method string
	// Pattern (see http://www.gorillatoolkit.org/pkg/mux)
	Pattern string
	// Handler (see documentation above)
	Handler RestHandler
	// This would be much cleaner if we could use types but
	// types are not first-class values
	// https://groups.google.com/forum/#!topic/golang-nuts/dYMlhyq5FpA
	// This is an attempt to bring what I think is a very useful model
	// from JAX-RS (https://jersey.java.net/documentation/latest/jaxrs-resources.html)

	// Specifies the type that this method takes as argument
	Consumes interface{}

	// Specifies the type that method returns
	Produces interface{}
}

// Each service defines routes
type Routes []Route

// PaniHandler interface to comply with http.Handler
type PaniHandler struct {
	paniHandler func(writer http.ResponseWriter, request *http.Request)
}

// ServeHTTP is required by
// https://golang.org/pkg/net/http/#Handler
func (paniHandler PaniHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	paniHandler.paniHandler(writer, request)
}

// For comparing to the type of Consumes field of Route struct
var requestType = reflect.TypeOf(http.Request{})

// wrapHandler wraps the RestHandler function, which deals
// with application logic into an instance of http.HandlerFunc
// which deals with raw HTTP request and response. The wrapper
// is intended to transparently deal with converting data to/from
// the wire format into internal representations.
func wrapHandler(restHandler RestHandler, consumes interface{}) http.Handler {
	consumesType := reflect.TypeOf(consumes)

	if consumesType == requestType {
		// This would mean the handler actually wants access to raw request/response
		// Fine, then...
		httpHandler := func(writer http.ResponseWriter, request *http.Request) {
			respReq := UnwrappedRestHandlerInput{writer, request}
			restHandler(respReq)
		}
		return PaniHandler{httpHandler}
	} else {
		httpHandler := func(writer http.ResponseWriter, request *http.Request) {
			// The "context" that magically appears here is one managed
			// by the mux -- here, Gorilla -- http://www.gorillatoolkit.org/pkg/context
			// but this is a fairly established middleware practice. The intent is
			// that the middleware augments the incoming request with some data
			// and then a downstream component (middleware or a final handler)
			// uses that data. For instance, in this case, we got the upstream
			// middleware (see UnmarshallerMiddleware) to unmarshal the data
			// into a consumable object, so we can use it.
			inDataMap := context.Get(request, ContextKeyUnmarshalledMap)
			var err error
			var inData interface{}
			if consumesType == nil {
				inData = ""
			} else {
				inData := reflect.New(consumesType)
				// This is where we decode a generic map into
				// actual objects that the code that deals with
				// logic can manipulate, relying on all the attendant
				// benefits (auto-completion, type-checking) that come
				// with using actual objects vs lists/dicts. See
				// https://github.com/mitchellh/mapstructure
				err = mapstructure.Decode(inDataMap, &inData)

				if err != nil {
					writer.WriteHeader(http.StatusInternalServerError)
					writer.Write([]byte(err.Error()))
					return
				}
			}

			outData, err := restHandler(inData)
			if err == nil {
				contentType := writer.Header().Get("Content-Type")
				// This should be ok because the middleware took care of negotiating
				// only the content types we support
				marshaller := ContentTypeMarshallers[contentType]
				if marshaller == nil {
					writer.WriteHeader(http.StatusUnsupportedMediaType)
					sct := getSupportedContentTypes()

					marshaller := ContentTypeMarshallers["application/json"]
					dataOut, _ := marshaller.Marshal(sct)
					writer.Write(dataOut)
					return
				}
				wireData, err := marshaller.Marshal(outData)
				if err == nil {
					writer.WriteHeader(http.StatusOK)
					writer.Write(wireData)
					return
				}
			}
			// There was an error -- that's the only way we fell through
			// here
			writer.WriteHeader(http.StatusInternalServerError)
			writer.Write([]byte(err.Error()))
		}
		return PaniHandler{httpHandler}
	}

}

// InitializeService initializes the service with the
// provided config and
func InitializeService(service Service, config ServiceConfig) (chan string, error) {
	channel := make(chan string)
	err := service.SetConfig(config)

	if err != nil {
		return nil, err
	}
	// Create negroni
	negroni := negroni.New()

	// Add authentication middleware
	negroni.Use(NewAuth())

	// Add content-negotiation middleware.
	// This is an example of using a middleware.
	// This will modify the response header to the
	// negotiated content type, and can then be used as
	// ct := w.Header().Get("Content-Type")
	// where w is http.ResponseWriter
	negroni.Use(NewNegotiator())

	// Unmarshal data from the content-type format
	// into a map
	negroni.Use(NewUnmarshaller())

	routes := service.Routes()
	router := newRouter(routes)
	negroni.UseHandler(router)

	hostPort := strings.Join([]string{config.Common.Api.Host, strconv.FormatUint(config.Common.Api.Port, 10)}, ":")
	go func() {
		channel <- "Starting."
		negroni.Run(hostPort)
	}()
	fmt.Println("Listening on " + hostPort)
	return channel, nil
}

// NewRouter creates router for a new service.
func newRouter(routes []Route) *mux.Router {
	router := mux.NewRouter().StrictSlash(true)
	for _, route := range routes {
		var handler RestHandler
		handler = route.Handler
		router.
			Methods(route.Method).
			Path(route.Pattern).
			//			Name(route.Name).
			Handler(wrapHandler(handler, route.Consumes))
	}
	return router
}

// TODO "text/html"
var supportedContentTypes = []string{"text/plain", "application/vnd.pani.v1+json", "application/vnd.pani+json", "application/json"}

// getSupportedContentTypes is just providing a struct
// to return in case of a 406 response
func getSupportedContentTypes() interface{} {
	type supportedContentTypesResponse struct {
		SupportedContentTypes []string `"json:supported_content_types"`
	}
	retval := supportedContentTypesResponse{}
	retval.SupportedContentTypes = supportedContentTypes
	return retval
}

type Marshaller interface {
	Marshal(v interface{}) ([]byte, error)
	Unmarshal(data []byte, v interface{}) error
}

type jsonMarshaller struct{}

func (j jsonMarshaller) Marshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func (j jsonMarshaller) Unmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// ContentTypeMarshallers maps MIME type to Marshaller instances
var ContentTypeMarshallers map[string]Marshaller = map[string]Marshaller{
	"application/json":             jsonMarshaller{},
	"application/vnd.pani.v1+json": jsonMarshaller{},
	"application/vnd.pani+json":    jsonMarshaller{},
}

// Authenticator is the interface that will be used by AuthMiddleware
// to provide authentication. Details to be worked out later.
type Authenticator interface {
	// As this is a placeholder, we are not dealing with
	// details yet, tokens vs credentials, principals vs roles, etc.
	Authenticate() Principal
}

type PlaceholderAuth struct {
}

func (p PlaceholderAuth) Authenticate() Principal {
	return Principal{"asdf", []string{"read", "write", "execute"}}
}

// Principal is a placeholder for a Principal structure
// for authentication. We'll leave roles and
// other stuff for later.
type Principal struct {
	Id string
	// This really is a placeholder
	Rights []string
}

// Wrapper for auth
type AuthMiddleware struct {
	Authenticator Authenticator
}

// NewAuth creates AuthMiddleware
func NewAuth() *AuthMiddleware {
	// Should we keep this global?
	auth := PlaceholderAuth{}
	return &AuthMiddleware{auth}
}

func (am AuthMiddleware) ServeHTTP(rw http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	p := am.Authenticator.Authenticate()
	rights := p.Rights
	context.Set(r, "Rights", rights)
	// Call the next middleware handler
	next(rw, r)
}

type UnmarshallerMiddleware struct {
}

func NewUnmarshaller() *UnmarshallerMiddleware {
	return &UnmarshallerMiddleware{}
}

type myReader struct{ *bytes.Buffer }

func (r myReader) Close() error { return nil }

const ContextKeyUnmarshalledMap string = "UnmarshalledMap"

// Unmarshals request body if needed. If not acceptable,
// returns an http.StatusNotAcceptable and this ends this
// request's lifecycle.
func (m UnmarshallerMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	ct := r.Header.Get("content-type")
	buf, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	if len(buf) == 0 {
		next(w, r)
		return
	}

	if marshaller, ok := ContentTypeMarshallers[ct]; ok {
		// Solution due to
		// http://stackoverflow.com/questions/23070876/reading-body-of-http-request-without-modifying-request-state
		// GG: I would not really judge this at all for this purpose until the
		// whole thing about how to use the middlewares settles.
		rdr2 := myReader{bytes.NewBuffer(buf)}
		r.Body = rdr2
		myMap := make(map[string]interface{})
		marshaller.Unmarshal(buf, &myMap)
		// TODO remove this...
		context.Set(r, ContextKeyUnmarshalledMap, myMap)
		// Call the next middleware handler
		next(w, r)
	} else {
		sct := getSupportedContentTypes()
		marshaller := ContentTypeMarshallers["application/json"]
		dataOut, _ := marshaller.Marshal(sct)
		w.WriteHeader(http.StatusNotAcceptable)
		w.Write(dataOut)
	}

}

type NegotiatorMiddleware struct {
}

func NewNegotiator() *NegotiatorMiddleware {
	return &NegotiatorMiddleware{}
}

func (negotiator NegotiatorMiddleware) ServeHTTP(writer http.ResponseWriter, request *http.Request, next http.HandlerFunc) {
	// TODO answer with a 406 here?
	accept := request.Header.Get("accept")
	format, err := negotiation.NegotiateAccept(accept, supportedContentTypes)
	if err == nil {
		writer.Header().Set("Content-Type", format.Value)
	}
	next(writer, request)
}