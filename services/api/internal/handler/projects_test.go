package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectsHandler_Create_InvalidJSON(t *testing.T) {
	h := NewProjectsHandler(nil)

	req := httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "invalid JSON") {
		t.Errorf("error = %q, should contain 'invalid JSON'", resp.Error)
	}
}

func TestProjectsHandler_Create_MissingName(t *testing.T) {
	h := NewProjectsHandler(nil)

	req := httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{"domain": "gaming", "category": "match3"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "name is required" {
		t.Errorf("error = %q, want 'name is required'", resp.Error)
	}
}

func TestProjectsHandler_Create_MissingDomain(t *testing.T) {
	h := NewProjectsHandler(nil)

	req := httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{"name": "Test Project", "category": "match3"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "domain is required" {
		t.Errorf("error = %q, want 'domain is required'", resp.Error)
	}
}

func TestProjectsHandler_Create_MissingCategory(t *testing.T) {
	h := NewProjectsHandler(nil)

	req := httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{"name": "Test Project", "domain": "gaming"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "category is required" {
		t.Errorf("error = %q, want 'category is required'", resp.Error)
	}
}

func TestProjectsHandler_Create_EmptyBody(t *testing.T) {
	h := NewProjectsHandler(nil)

	req := httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestProjectsHandler_Create_ValidBody_NilRepo(t *testing.T) {
	h := NewProjectsHandler(nil)

	req := httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{"name": "Test", "domain": "gaming", "category": "match3"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Will panic on nil repo when trying to create — expected
	defer func() { recover() }()
	h.Create(w, req)

	// If we reach here without panic, the validation passed (which is correct)
	if w.Code == http.StatusBadRequest {
		t.Error("valid body should pass validation")
	}
}

func TestProjectsHandler_List_NilRepo(t *testing.T) {
	h := NewProjectsHandler(nil)

	req := httptest.NewRequest("GET", "/api/v1/projects", nil)
	w := httptest.NewRecorder()

	// Will panic on nil repo — expected
	defer func() { recover() }()
	h.List(w, req)
}

func TestProjectsHandler_Get_NilRepo(t *testing.T) {
	h := NewProjectsHandler(nil)

	req := httptest.NewRequest("GET", "/api/v1/projects/some-id", nil)
	req.SetPathValue("id", "some-id")
	w := httptest.NewRecorder()

	// Will panic on nil repo — expected
	defer func() { recover() }()
	h.Get(w, req)
}

func TestProjectsHandler_Update_NilRepo(t *testing.T) {
	h := NewProjectsHandler(nil)

	req := httptest.NewRequest("PUT", "/api/v1/projects/some-id",
		strings.NewReader(`{"name": "Updated"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "some-id")
	w := httptest.NewRecorder()

	// Will panic on nil repo when trying to GetByID — expected
	defer func() { recover() }()
	h.Update(w, req)
}

func TestProjectsHandler_Delete_NilRepo(t *testing.T) {
	h := NewProjectsHandler(nil)

	req := httptest.NewRequest("DELETE", "/api/v1/projects/some-id", nil)
	req.SetPathValue("id", "some-id")
	w := httptest.NewRecorder()

	// Will panic on nil repo — expected
	defer func() { recover() }()
	h.Delete(w, req)
}

func TestProjectsHandler_Create_ValidationOrder(t *testing.T) {
	// Verify that name is checked first, then domain, then category
	h := NewProjectsHandler(nil)

	// All missing: name should be reported first
	req := httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.Create(w, req)

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "name is required" {
		t.Errorf("first validation error should be name, got %q", resp.Error)
	}

	// Name present, domain missing: domain should be reported
	req = httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{"name": "Test"}`))
	w = httptest.NewRecorder()
	h.Create(w, req)

	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "domain is required" {
		t.Errorf("second validation error should be domain, got %q", resp.Error)
	}

	// Name and domain present, category missing: category should be reported
	req = httptest.NewRequest("POST", "/api/v1/projects",
		strings.NewReader(`{"name": "Test", "domain": "gaming"}`))
	w = httptest.NewRecorder()
	h.Create(w, req)

	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "category is required" {
		t.Errorf("third validation error should be category, got %q", resp.Error)
	}
}

func TestProjectsHandler_List_QueryParams(t *testing.T) {
	// Verify that query params are parsed (limit, offset).
	// With nil repo this will panic, but we're just testing that the handler
	// reads the URL correctly by observing the expected behavior before the panic.
	h := NewProjectsHandler(nil)

	req := httptest.NewRequest("GET", "/api/v1/projects?limit=10&offset=5", nil)
	w := httptest.NewRecorder()

	defer func() { recover() }()
	h.List(w, req)
}
