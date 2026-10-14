package webgw

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"

	"github.com/artyomsv/quil/internal/ipc"
)

const (
	settingsBodyMax = 64 << 10
	// fileBodyMax covers FileTextMax of text after JSON escaping (at most 6
	// bytes per byte, \u00XX) plus the other fields. The text itself is
	// checked against FileTextMax after decoding.
	fileBodyMax = 6*FileTextMax + 4<<10
)

var logNames = map[string]bool{"client": true, "daemon": true, "mcp": true, "web": true}

// filesGate answers 404 without Files, runs apiAuth, then the rights rule:
// full, or standard too when allowStandard (GET /api/settings only). Every
// refusal happens before Files is called.
func (s *Server) filesGate(w http.ResponseWriter, r *http.Request, allowStandard bool) bool {
	if s.cfg.Files == nil {
		w.WriteHeader(http.StatusNotFound)
		return false
	}
	session, ok := s.apiAuth(w, r)
	if !ok {
		return false
	}
	rights := s.sessionRights(session)
	if rights == ipc.RightsFull || (allowStandard && rights == ipc.RightsStandard) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "needs full rights"})
	return false
}

// decodeBody reads a JSON body of at most limit bytes into v: 415 for another
// type, 413 past the cap, 400 for anything that does not decode.
func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "too large"})
			return false
		}
		w.WriteHeader(http.StatusBadRequest)
		return false
	}
	return true
}

// fileErr answers a Files error: a FileError as its status and text, anything
// else as 500 with the detail in web.log only.
func (s *Server) fileErr(w http.ResponseWriter, err error) {
	var fe *FileError
	if errors.As(err, &fe) {
		body := map[string]any{"error": fe.Text}
		if fe.Current != nil {
			body["current"] = fe.Current
		}
		writeJSON(w, fe.Status, body)
		return
	}
	s.cfg.Logf("web files: %v", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed; see web.log"})
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	w.WriteHeader(http.StatusMethodNotAllowed)
}

// handleSettings is GET (standard or full) and PATCH {key, value} (full).
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodPatch:
	default:
		methodNotAllowed(w, "GET, PATCH")
		return
	}
	if !s.filesGate(w, r, r.Method == http.MethodGet) {
		return
	}
	var view SettingsView
	var err error
	if r.Method == http.MethodGet {
		view, err = s.cfg.Files.Settings()
	} else {
		var b struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		if !decodeBody(w, r, settingsBodyMax, &b) {
			return
		}
		if b.Key == "" || len(b.Key) > 128 || len(b.Value) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		view, err = s.cfg.Files.SetSetting(b.Key, b.Value)
	}
	if err != nil {
		s.fileErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

type textBody struct {
	Text     string `json:"text"`
	BaseHash string `json:"base_hash"`
	Force    bool   `json:"force"`
}

// serveText is GET and PUT of one text file.
func (s *Server) serveText(w http.ResponseWriter, r *http.Request, t FileTarget) {
	var tf TextFile
	var err error
	if r.Method == http.MethodGet {
		tf, err = s.cfg.Files.ReadText(t)
	} else {
		var b textBody
		if !decodeBody(w, r, fileBodyMax, &b) {
			return
		}
		if len(b.Text) > FileTextMax {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "too large"})
			return
		}
		tf, err = s.cfg.Files.WriteText(t, b.Text, b.BaseHash, b.Force)
	}
	if err != nil {
		s.fileErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tf)
}

func (s *Server) handleTemplatesFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		methodNotAllowed(w, "GET, PUT")
		return
	}
	if !s.filesGate(w, r, false) {
		return
	}
	s.serveText(w, r, FileTarget{Kind: TargetTemplates})
}

func (s *Server) handlePluginFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		methodNotAllowed(w, "GET, PUT")
		return
	}
	if !s.filesGate(w, r, false) {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/files/plugins/")
	if !PluginNameOK(name) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.serveText(w, r, FileTarget{Kind: TargetPlugin, Name: name})
}

func (s *Server) handleMigrationList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	if !s.filesGate(w, r, false) {
		return
	}
	files, err := s.cfg.Files.Migration()
	if err != nil {
		s.fileErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]StaleFile{"files": nonNilStale(files)})
}

func (s *Server) handleMigrationResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	if !s.filesGate(w, r, false) {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/migration/")
	if !PluginNameOK(name) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var b struct {
		Choice string `json:"choice"`
		Text   string `json:"text"`
	}
	if !decodeBody(w, r, fileBodyMax, &b) {
		return
	}
	if len(b.Text) > FileTextMax {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "too large"})
		return
	}
	files, err := s.cfg.Files.ResolveMigration(name, b.Choice, b.Text)
	if err != nil {
		s.fileErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]StaleFile{"files": nonNilStale(files)})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	if !s.filesGate(w, r, false) {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/logs/")
	if !logNames[name] {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	lt, err := s.cfg.Files.LogTail(name)
	if err != nil {
		s.fileErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lt)
}

func nonNilStale(f []StaleFile) []StaleFile {
	if f == nil {
		return []StaleFile{}
	}
	return f
}
