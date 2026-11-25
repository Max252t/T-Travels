package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

func TestHealthContract(t *testing.T) {
	router := newRouter(nil)
	contract := loadContract(t, router)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health response: status %d, body %s", response.Code, response.Body)
	}
	validateResponse(t, contract, request, response)
}

func loadContract(t *testing.T, router http.Handler) routers.Router {
	t.Helper()
	spec := httptest.NewRecorder()
	router.ServeHTTP(spec, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))
	if spec.Code != http.StatusOK || spec.Header().Get("Content-Type") != "application/yaml" {
		t.Fatalf("OpenAPI response: status %d, Content-Type %q", spec.Code, spec.Header().Get("Content-Type"))
	}

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(spec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	contract, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func validateResponse(t *testing.T, contract routers.Router, request *http.Request, response *httptest.ResponseRecorder) {
	t.Helper()
	route, pathParams, err := contract.FindRoute(request)
	if err != nil {
		t.Fatal(err)
	}
	validation := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request:    request,
			Route:      route,
			PathParams: pathParams,
		},
		Status:  response.Code,
		Header:  response.Header(),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	validation.SetBodyBytes(response.Body.Bytes())
	if err := openapi3filter.ValidateResponse(t.Context(), validation); err != nil {
		t.Fatal(err)
	}
}
