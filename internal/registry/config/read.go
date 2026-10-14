package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The repository's layout. Each kind has a directory with one <name>.yaml
// file per object. A skill is a directory skills/<name>/ with skill.yaml and
// one directory per version. The yank list is yanks.yaml at the top.
var kindDirs = []struct {
	dir  string
	kind Kind
}{
	{"agents", KindAgent},
	{"servers", KindMcpServer},
	{"work-item-types", KindWorkItemType},
	{"trigger-mappings", KindTriggerMapping},
	{"identity-sources", KindIdentitySource},
	{"redaction-rules", KindRedactionRule},
}

const (
	skillsDir = "skills"
	skillFile = "skill.yaml"
	yanksFile = "yanks.yaml"
	// skillEntry is the file a runtime reads first in every skill version.
	skillEntry = "SKILL.md"

	// Size limits keep a revision cheap to load and to send to a model.
	maxFileBytes   = 1 << 20
	maxBundleBytes = 10 << 20
	maxBundleFiles = 500
)

// index holds the objects of one revision as read, before the rules run.
type index struct {
	fsys       fs.FS
	files      map[string]string // configuration file path to content hash
	agents     map[string]*Agent
	skills     map[string]*Skill
	servers    map[string]*McpServer
	workTypes  map[string]*WorkItemType
	mappings   map[string]*TriggerMapping
	identities map[string]*IdentitySource
	redactions map[string]*RedactionRule
	yanks      *YankList
}

// header is what every kind with metadata exposes to the reader.
type header interface {
	head() (Kind, *Metadata)
	setOrigin(file, hash string)
}

func (o *origin) setOrigin(file, hash string) { o.file, o.hash = file, hash }

func (a *Agent) head() (Kind, *Metadata)          { return a.Kind, &a.Metadata }
func (s *Skill) head() (Kind, *Metadata)          { return s.Kind, &s.Metadata }
func (s *McpServer) head() (Kind, *Metadata)      { return s.Kind, &s.Metadata }
func (w *WorkItemType) head() (Kind, *Metadata)   { return w.Kind, &w.Metadata }
func (m *TriggerMapping) head() (Kind, *Metadata) { return m.Kind, &m.Metadata }
func (i *IdentitySource) head() (Kind, *Metadata) { return i.Kind, &i.Metadata }
func (r *RedactionRule) head() (Kind, *Metadata)  { return r.Kind, &r.Metadata }

// read reads every configuration file of fsys. It reports layout and decoding
// problems; the rules run later, over the index.
func read(fsys fs.FS, ps *problems) *index {
	ix := &index{
		fsys: fsys, files: map[string]string{},
		agents: map[string]*Agent{}, skills: map[string]*Skill{}, servers: map[string]*McpServer{},
		workTypes: map[string]*WorkItemType{}, mappings: map[string]*TriggerMapping{},
		identities: map[string]*IdentitySource{}, redactions: map[string]*RedactionRule{},
	}
	ix.readTop(ps)
	for _, kd := range kindDirs {
		ix.readKind(kd.dir, kd.kind, ps)
	}
	ix.readSkills(ps)
	ix.readYanks(ps)
	return ix
}

// readTop refuses top-level entries that look like configuration but are not
// read, so that nothing is silently ignored. Hidden entries and other files,
// such as README.md, are allowed.
func (ix *index) readTop(ps *problems) {
	entries, err := fs.ReadDir(ix.fsys, ".")
	if err != nil {
		ps.add(".", "", "cannot be read: %v", err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		known := name == skillsDir || slices.ContainsFunc(kindDirs, func(kd struct {
			dir  string
			kind Kind
		}) bool {
			return kd.dir == name
		})
		switch {
		case strings.HasPrefix(name, "."), known, name == yanksFile:
		case e.IsDir():
			ps.add(name, "", "is not a configuration directory; use agents, servers, work-item-types, trigger-mappings, identity-sources, redaction-rules or skills")
		case strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml"):
			ps.add(name, "", "is not read; objects go in their kind's directory")
		}
	}
}

func (ix *index) readKind(dir string, kind Kind, ps *problems) {
	entries, err := fs.ReadDir(ix.fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		ps.add(dir, "", "cannot be read: %v", err)
		return
	}
	for _, e := range entries {
		file := path.Join(dir, e.Name())
		name, isYAML := strings.CutSuffix(e.Name(), ".yaml")
		if !e.Type().IsRegular() || !isYAML {
			ps.add(file, "", "must be a regular file named <name>.yaml")
			continue
		}
		data, ok := ix.readFile(file, maxFileBytes, ps)
		if !ok {
			continue
		}
		ix.files[file] = hashBytes(data)
		switch kind {
		case KindAgent:
			put(ix.agents, name, &Agent{}, data, file, kind, true, ps)
		case KindMcpServer:
			put(ix.servers, name, &McpServer{}, data, file, kind, false, ps)
		case KindWorkItemType:
			put(ix.workTypes, name, &WorkItemType{}, data, file, kind, false, ps)
		case KindTriggerMapping:
			put(ix.mappings, name, &TriggerMapping{}, data, file, kind, false, ps)
		case KindIdentitySource:
			put(ix.identities, name, &IdentitySource{}, data, file, kind, false, ps)
		case KindRedactionRule:
			put(ix.redactions, name, &RedactionRule{}, data, file, kind, false, ps)
		}
	}
}

// put decodes one object file and indexes the object by name if it decodes.
func put[T header](m map[string]T, name string, obj T, data []byte, file string, want Kind, person bool, ps *problems) {
	if decodeObject(data, file, name, want, person, obj, ps) {
		m[name] = obj
	}
}

// decodeObject decodes one object file and checks its kind, name and owner.
// A person must own agents and skills.
func decodeObject(data []byte, file, name string, want Kind, person bool, obj header, ps *problems) bool {
	if err := decodeYAML(data, obj); err != nil {
		ps.add(file, "", "%s", err)
		return false
	}
	kind, md := obj.head()
	ok := true
	if kind != want {
		ps.add(file, "kind", "must be %s in this directory, got %q", want, kind)
		ok = false
	}
	if !objectName.MatchString(name) {
		ps.add(file, "", "the file name must be a lowercase DNS label, such as release-investigator")
		ok = false
	} else if md.Name != name {
		ps.add(file, "metadata.name", "must match the file name %q, got %q", name, md.Name)
		ok = false
	}
	if err := checkOwner(md.Owner, person); err != nil {
		ps.add(file, "metadata.owner", "%s", err)
		ok = false
	}
	obj.setOrigin(file, hashBytes(data))
	return ok
}

// decodeYAML decodes one YAML document strictly: unknown fields, repeated
// keys and further documents are refused.
func decodeYAML(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("the file is empty")
		}
		return errors.New(yamlMessage(err))
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return errors.New("the file must hold exactly one YAML document")
	}
	return nil
}

// yamlMessage flattens the YAML library's multi-line errors into one line.
func yamlMessage(err error) string {
	msg := strings.TrimPrefix(err.Error(), "yaml: unmarshal errors:\n")
	msg = strings.TrimPrefix(msg, "yaml: ")
	return strings.Join(strings.Fields(strings.ReplaceAll(msg, "\n", ";")), " ")
}

func (ix *index) readSkills(ps *problems) {
	entries, err := fs.ReadDir(ix.fsys, skillsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		ps.add(skillsDir, "", "cannot be read: %v", err)
		return
	}
	for _, e := range entries {
		dir := path.Join(skillsDir, e.Name())
		if !e.IsDir() {
			ps.add(dir, "", "must be a directory holding skill.yaml and one directory per version")
			continue
		}
		file := path.Join(dir, skillFile)
		data, ok := ix.readFile(file, maxFileBytes, ps)
		if !ok {
			continue
		}
		ix.files[file] = hashBytes(data)
		s := &Skill{}
		if !decodeObject(data, file, e.Name(), KindSkill, true, s, ps) {
			continue
		}
		ix.skills[s.Metadata.Name] = s
		ix.readBundles(dir, s, ps)
	}
}

// readBundles reads the bundle of every declared version and refuses
// directories that no version declares.
func (ix *index) readBundles(dir string, s *Skill, ps *problems) {
	entries, err := fs.ReadDir(ix.fsys, dir)
	if err != nil {
		ps.add(dir, "", "cannot be read: %v", err)
		return
	}
	for _, e := range entries {
		if e.Name() == skillFile {
			continue
		}
		v := s.version(e.Name())
		if !e.IsDir() || v == nil {
			ps.add(path.Join(dir, e.Name()), "", "is neither skill.yaml nor the directory of a version that skill.yaml declares")
			continue
		}
		ix.readBundle(path.Join(dir, e.Name()), v, ps)
	}
}

// readBundle reads one version's files, in lexical order, and computes its
// digest over their paths and contents.
func (ix *index) readBundle(dir string, v *SkillVersion, ps *problems) {
	h := sha256.New()
	var total int64
	err := fs.WalkDir(ix.fsys, dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			ps.add(p, "", "is not a regular file; skill bundles hold no links")
			return nil
		}
		data, ok := ix.readFile(p, maxBundleBytes, ps)
		if !ok {
			return nil
		}
		rel := strings.TrimPrefix(p, dir+"/")
		v.files = append(v.files, rel)
		total += int64(len(data))
		sum := sha256.Sum256(data)
		_, _ = fmt.Fprintf(h, "%s\x00%x\n", rel, sum)
		return nil
	})
	switch {
	case err != nil:
		ps.add(dir, "", "cannot be read: %v", err)
	case len(v.files) > maxBundleFiles:
		ps.add(dir, "", "holds %d files; a version holds at most %d", len(v.files), maxBundleFiles)
	case total > maxBundleBytes:
		ps.add(dir, "", "holds %d bytes; a version holds at most %d", total, maxBundleBytes)
	case !slices.Contains(v.files, skillEntry):
		ps.add(dir, "", "has no %s", skillEntry)
	}
	v.digest = "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func (ix *index) readYanks(ps *problems) {
	data, ok := ix.readOptional(yanksFile, ps)
	if !ok {
		return
	}
	y := &YankList{}
	if err := decodeYAML(data, y); err != nil {
		ps.add(yanksFile, "", "%s", err)
		return
	}
	if y.Kind != KindYankList {
		ps.add(yanksFile, "kind", "must be %s, got %q", KindYankList, y.Kind)
		return
	}
	y.setOrigin(yanksFile, hashBytes(data))
	ix.files[yanksFile] = y.hash
	ix.yanks = y
}

// readOptional reads a file that may be absent.
func (ix *index) readOptional(name string, ps *problems) ([]byte, bool) {
	if _, err := fs.Stat(ix.fsys, name); errors.Is(err, fs.ErrNotExist) {
		return nil, false
	}
	return ix.readFile(name, maxFileBytes, ps)
}

// readFile reads a file of at most limit bytes. Configuration files are
// recorded with their content hashes.
func (ix *index) readFile(name string, limit int64, ps *problems) ([]byte, bool) {
	info, err := fs.Stat(ix.fsys, name)
	switch {
	case err != nil:
		ps.add(name, "", "cannot be read: %v", err)
		return nil, false
	case !info.Mode().IsRegular():
		ps.add(name, "", "is not a regular file")
		return nil, false
	case info.Size() > limit:
		ps.add(name, "", "is larger than %d bytes", limit)
		return nil, false
	}
	data, err := fs.ReadFile(ix.fsys, name)
	if err != nil {
		ps.add(name, "", "cannot be read: %v", err)
		return nil, false
	}
	return data, true
}

// hashBytes returns the SHA-256 of data as sha256:<hex>.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// version returns the declared version with the given label, or nil.
func (s *Skill) version(label string) *SkillVersion {
	for _, v := range s.Spec.Versions {
		if v != nil && v.Version == label {
			return v
		}
	}
	return nil
}

// skillVersion returns a released version, or nil.
func (ix *index) skillVersion(ref SkillRef) *SkillVersion {
	s := ix.skills[ref.Skill]
	if s == nil {
		return nil
	}
	return s.version(ref.Version)
}
