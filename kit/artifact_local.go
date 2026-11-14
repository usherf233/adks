package kit

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/v2/artifact"
	"google.golang.org/genai"
)

// localArtifactService stores artifacts as files.
//
// ADK Go ships only a Cloud Storage implementation, which makes "run this on my
// laptop" require a cloud project. This is the missing local backend: one
// directory per artifact, one file per version, so a human can open the folder
// and see what the agent produced.
//
//	{root}/{app}/{user}/{session}/{file}/v{N}{ext}
//	{root}/{app}/{user}/{session}/{file}/meta.json
type localArtifactService struct {
	root string
	mu   sync.Mutex
}

// NewLocalArtifactService creates the root directory if needed.
func NewLocalArtifactService(root string) (artifact.Service, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("local artifact service: root directory is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("local artifact service: resolve %q: %w", root, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("local artifact service: create %q: %w", abs, err)
	}
	return &localArtifactService{root: abs}, nil
}

type artifactMeta struct {
	Version        int64          `json:"version"`
	MimeType       string         `json:"mimeType"`
	CreateTime     time.Time      `json:"createTime"`
	CanonicalURI   string         `json:"canonicalURI"`
	CustomMetadata map[string]any `json:"customMetadata"`
}

// dirFor validates every path component: a file name arrives from a model, and
// "../../etc/passwd" must not become a path.
func (s *localArtifactService) dirFor(app, user, session, file string) (string, error) {
	for _, part := range []string{app, user, session, file} {
		if part == "" {
			return "", fmt.Errorf("artifact path component is empty")
		}
		if strings.ContainsAny(part, `/\`) || part == "." || part == ".." {
			return "", fmt.Errorf("invalid artifact path component %q", part)
		}
	}
	return filepath.Join(s.root, app, user, session, file), nil
}

func (s *localArtifactService) Save(_ context.Context, req *artifact.SaveRequest) (*artifact.SaveResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	data, mimeType, err := partBytes(req.Part)
	if err != nil {
		return nil, err
	}
	dir, err := s.dirFor(req.AppName, req.UserID, req.SessionID, req.FileName)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	versions, err := listVersions(dir)
	if err != nil {
		return nil, err
	}
	next := int64(1)
	if len(versions) > 0 {
		next = versions[len(versions)-1] + 1
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("artifact save: %w", err)
	}
	path := filepath.Join(dir, versionName(next, mimeType))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, fmt.Errorf("artifact save: %w", err)
	}

	meta := artifactMeta{
		Version:      next,
		MimeType:     mimeType,
		CreateTime:   time.Now(),
		CanonicalURI: "file:///" + filepath.ToSlash(path),
	}
	if len(req.CustomMetadata) > 0 {
		meta.CustomMetadata = make(map[string]any, len(req.CustomMetadata))
		for k, v := range req.CustomMetadata {
			meta.CustomMetadata[k] = fmt.Sprint(v)
		}
	}
	if err := writeMeta(dir, meta); err != nil {
		return nil, err
	}
	return &artifact.SaveResponse{Version: next}, nil
}

func (s *localArtifactService) Load(_ context.Context, req *artifact.LoadRequest) (*artifact.LoadResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	dir, err := s.dirFor(req.AppName, req.UserID, req.SessionID, req.FileName)
	if err != nil {
		return nil, err
	}
	versions, err := listVersions(dir)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("artifact %q not found", req.FileName)
	}
	want := req.Version
	if want == 0 {
		want = versions[len(versions)-1]
	}
	path, ok := findVersionFile(dir, want)
	if !ok {
		return nil, fmt.Errorf("artifact %q version %d not found", req.FileName, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("artifact load: %w", err)
	}
	meta, _ := readMeta(dir)
	mimeType := meta.MimeType
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(path))
	}
	return &artifact.LoadResponse{Part: &genai.Part{InlineData: &genai.Blob{Data: data, MIMEType: mimeType}}}, nil
}

func (s *localArtifactService) Delete(_ context.Context, req *artifact.DeleteRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	dir, err := s.dirFor(req.AppName, req.UserID, req.SessionID, req.FileName)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.Version == 0 {
		// Missing artifacts are not an error, matching the interface contract.
		return os.RemoveAll(dir)
	}
	if path, ok := findVersionFile(dir, req.Version); ok {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("artifact delete: %w", err)
		}
	}
	return nil
}

func (s *localArtifactService) List(_ context.Context, req *artifact.ListRequest) (*artifact.ListResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	// Save/Load/Delete all go through dirFor's component checks. List used to
	// join the three components directly, so a ".." could list outside root.
	for _, part := range []string{req.AppName, req.UserID, req.SessionID} {
		if part == "" || strings.ContainsAny(part, `/\`) || part == "." || part == ".." {
			return nil, fmt.Errorf("invalid artifact path component %q", part)
		}
	}
	sessionDir := filepath.Join(s.root, req.AppName, req.UserID, req.SessionID)
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &artifact.ListResponse{}, nil
		}
		return nil, fmt.Errorf("artifact list: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return &artifact.ListResponse{FileNames: names}, nil
}

func (s *localArtifactService) Versions(_ context.Context, req *artifact.VersionsRequest) (*artifact.VersionsResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	dir, err := s.dirFor(req.AppName, req.UserID, req.SessionID, req.FileName)
	if err != nil {
		return nil, err
	}
	versions, err := listVersions(dir)
	if err != nil {
		return nil, err
	}
	return &artifact.VersionsResponse{Versions: versions}, nil
}

func (s *localArtifactService) GetArtifactVersion(_ context.Context, req *artifact.GetArtifactVersionRequest) (*artifact.GetArtifactVersionResponse, error) {
	dir, err := s.dirFor(req.AppName, req.UserID, req.SessionID, req.FileName)
	if err != nil {
		return nil, err
	}
	versions, err := listVersions(dir)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("artifact %q not found", req.FileName)
	}
	meta, _ := readMeta(dir)
	want := req.Version
	if want == 0 {
		want = versions[len(versions)-1]
	}
	path, ok := findVersionFile(dir, want)
	if !ok {
		return nil, fmt.Errorf("artifact %q version %d not found", req.FileName, want)
	}
	av := &artifact.ArtifactVersion{
		Version:        want,
		CanonicalURI:   "file:///" + filepath.ToSlash(path),
		CustomMetadata: map[string]any{},
		CreateTime:     meta.CreateTime,
		MimeType:       meta.MimeType,
	}
	if meta.Version == want {
		for k, v := range meta.CustomMetadata {
			av.CustomMetadata[k] = v
		}
	}
	if av.CreateTime.IsZero() {
		if st, err := os.Stat(path); err == nil {
			av.CreateTime = st.ModTime()
		}
	}
	return &artifact.GetArtifactVersionResponse{ArtifactVersion: av}, nil
}

func partBytes(p *genai.Part) ([]byte, string, error) {
	switch {
	case p.InlineData != nil:
		mt := p.InlineData.MIMEType
		if mt == "" {
			mt = "application/octet-stream"
		}
		return p.InlineData.Data, mt, nil
	case p.Text != "":
		return []byte(p.Text), "text/plain", nil
	}
	return nil, "", fmt.Errorf("artifact has neither inline data nor text")
}

func extFor(mimeType string) string {
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}

func versionName(v int64, mimeType string) string {
	return fmt.Sprintf("v%d%s", v, extFor(mimeType))
}

func listVersions(dir string) ([]int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("artifact versions: %w", err)
	}
	var out []int64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "v") || e.IsDir() {
			continue
		}
		base := strings.TrimSuffix(name, filepath.Ext(name))
		if n, err := strconv.ParseInt(base[1:], 10, 64); err == nil {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func findVersionFile(dir string, version int64) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	prefix := fmt.Sprintf("v%d.", version)
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			return filepath.Join(dir, e.Name()), true
		}
	}
	return "", false
}

func metaPath(dir string) string { return filepath.Join(dir, "meta.json") }

func writeMeta(dir string, meta artifactMeta) error {
	buf, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("artifact meta: %w", err)
	}
	return os.WriteFile(metaPath(dir), buf, 0o644)
}

func readMeta(dir string) (artifactMeta, error) {
	var meta artifactMeta
	buf, err := os.ReadFile(metaPath(dir))
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(buf, &meta); err != nil {
		return meta, err
	}
	return meta, nil
}
