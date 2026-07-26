/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// implements a conformance test server that speaks both the Envoy ext_authz
// v3 gRPC protocol and the HTTP external-auth protocol.
//
// Authorization logic: requests whose path equals /allowed are permitted;
// all other paths are denied with a 403 / PermissionDenied response.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

const (
	grpcAllowedPath = "/grpc/allowed"
	httpAllowedPath = "/http/allowed"
	deniedBody      = "Access denied by external auth server"
)

type grpcAuthServer struct {
	authv3.UnimplementedAuthorizationServer
}

func (s *grpcAuthServer) Check(_ context.Context, req *authv3.CheckRequest) (*authv3.CheckResponse, error) {
	path := req.GetAttributes().GetRequest().GetHttp().GetPath()
	fmt.Printf("grpc check: path=%q\n", path)

	if path == grpcAllowedPath {
		return &authv3.CheckResponse{
			Status: &statuspb.Status{Code: int32(codes.OK)},
			HttpResponse: &authv3.CheckResponse_OkResponse{
				OkResponse: &authv3.OkHttpResponse{},
			},
		}, nil
	}

	return &authv3.CheckResponse{
		Status: &statuspb.Status{Code: int32(codes.PermissionDenied)},
		HttpResponse: &authv3.CheckResponse_DeniedResponse{
			DeniedResponse: &authv3.DeniedHttpResponse{
				Status: &typev3.HttpStatus{Code: typev3.StatusCode_Forbidden},
				Body:   deniedBody,
			},
		},
	}, nil
}

func httpAuthHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	fmt.Printf("http check: path=%q\n", path)

	if path == httpAllowedPath {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusForbidden)
	_, _ = fmt.Fprint(w, deniedBody)
}

func envPort(envVar string, defaultPort int) int {
	if v := os.Getenv(envVar); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			fmt.Printf("non-integer value in %s %q: %v\n", envVar, v, err)
			os.Exit(1)
		}
		return p
	}
	return defaultPort
}

func main() {
	grpcPort := envPort("GRPC_PORT", 9001)
	httpPort := envPort("HTTP_PORT", 9002)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start gRPC ext_authz server.
	lc := net.ListenConfig{}
	grpcLis, err := lc.Listen(ctx, "tcp", fmt.Sprintf("0.0.0.0:%d", grpcPort))
	if err != nil {
		fmt.Printf("grpc: failed to listen: %v\n", err)
		os.Exit(1)
	}
	grpcServer := grpc.NewServer()
	authv3.RegisterAuthorizationServer(grpcServer, &grpcAuthServer{})
	fmt.Printf("grpc ext_authz server listening on %s\n", grpcLis.Addr())
	go func() {
		if err := grpcServer.Serve(grpcLis); err != nil {
			fmt.Printf("grpc: serve error: %v\n", err)
			os.Exit(1)
		}
	}()

	// Start HTTP external-auth server.
	mux := http.NewServeMux()
	mux.HandleFunc("/", httpAuthHandler)
	httpServer := &http.Server{Addr: fmt.Sprintf("0.0.0.0:%d", httpPort), Handler: mux} //nolint:gosec
	fmt.Printf("http ext_authz server listening on %s\n", httpServer.Addr)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("http: serve error: %v\n", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	grpcServer.GracefulStop()
	_ = httpServer.Shutdown(context.Background()) //nolint:contextcheck
}
