// Package route53test implements a fake Route53 provider with the capability of
// inducing errors on any given operation, and retrospectively determining what
// operations have been carried out.
package route53test

import (
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pivotal-cloudops/cloudops-goamz/route53"
)

type action struct {
	srv   *Server
	w     http.ResponseWriter
	req   *http.Request
	reqId string
}

type Server struct {
	reqId    int
	url      string
	listener net.Listener
	mutex    sync.Mutex
	records  []route53.ResourceRecordSet
}

func NewServer() (*Server, error) {
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return nil, fmt.Errorf("cannot listen on localhost: %v", err)
	}
	srv := &Server{
		listener: l,
		url:      "http://" + l.Addr().String(),
	}
	go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		srv.serveHTTP(w, req)
	}))
	return srv, nil
}

func (srv *Server) Quit() error {
	return srv.listener.Close()
}

func (srv *Server) URL() string {
	return srv.url
}

type Error struct {
	StatusCode int
	Code       string
	Message    string
}

func (e Error) Error() string {
	return fmt.Sprintf("%d: %s", e.StatusCode, e.Message)
}

func (srv *Server) handleError(w http.ResponseWriter, err error) {
	fmt.Println(err.Error())

	if err, ok := err.(Error); ok {
		w.WriteHeader(err.StatusCode)
		w.Write([]byte(err.Message))
	} else {
		w.WriteHeader(500)
		panic("Server error: " + err.Error())
	}

}

func (srv *Server) listResourceRecordSets(w http.ResponseWriter, req *http.Request, reqID string) (interface{}, error) {
	return route53.ListResourceRecordSetsResponse{
		Records: srv.records,
	}, nil
}

func (srv *Server) changeResourceRecordSets(w http.ResponseWriter, req *http.Request, reqID string) (interface{}, error) {
	var changeRequest route53.ChangeResourceRecordSetsRequest
	if err := xml.NewDecoder(req.Body).Decode(&changeRequest); err != nil {
		return nil, err
	}
	for _, change := range changeRequest.Changes {
		switch change.Action {
		case "CREATE":
			srv.records = append(srv.records, change.Record)
		case "DELETE":
			for i, record := range srv.records {
				if record.Name == change.Record.Name {
					srv.records = append(srv.records[:i], srv.records[i+1:]...)
				}
			}
		}
	}
	return route53.ChangeResourceRecordSetsResponse{
		ChangeInfo: route53.ChangeInfo{
			ID:          "some-id",
			Status:      "some-status",
			SubmittedAt: time.Now().Format("2006-01-02T15:04:05Z"),
		},
	}, nil
}

type xmlErrors struct {
	XMLName string `xml:"ErrorResponse"`
	Error   Error
}

func (srv *Server) error(w http.ResponseWriter, err *Error) {
	w.WriteHeader(err.StatusCode)
	xmlErr := xmlErrors{Error: *err}
	if e := xml.NewEncoder(w).Encode(xmlErr); e != nil {
		panic(e)
	}
}

func (srv *Server) serveHTTP(w http.ResponseWriter, req *http.Request) {
	req.ParseForm()
	srv.mutex.Lock()
	defer srv.mutex.Unlock()
	method := req.Method
	resource := strings.Split(req.URL.Path, "/")[4]
	f := actions[resource][method]
	if f == nil {
		srv.error(w, &Error{
			StatusCode: 400,
			Code:       "InvalidParameterValue",
			Message:    "Unrecognized Action",
		})
		fmt.Printf("Fake Route53 server doesn't know how to: %s %s\n", method, resource)
		return
	}
	reqId := fmt.Sprintf("req%0X", srv.reqId)
	srv.reqId++
	if resp, err := f(srv, w, req, reqId); err == nil {
		if err := xml.NewEncoder(w).Encode(resp); err != nil {
			panic(err)
		}
	} else {
		switch err.(type) {
		case *Error:
			srv.error(w, err.(*Error))
		default:
			panic(err)
		}
	}
}

type actionMethods map[string]func(*Server, http.ResponseWriter, *http.Request, string) (interface{}, error)

var actions = map[string]actionMethods{
	"rrset": {
		"GET":  (*Server).listResourceRecordSets,
		"POST": (*Server).changeResourceRecordSets,
	},
}
