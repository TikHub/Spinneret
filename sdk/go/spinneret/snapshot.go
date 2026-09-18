package spinneret

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
)

const (
	// snapshotFormat is the version of the snapshot file layout (shared with the Python SDK).
	snapshotFormat = 1
	// snapshotTokenNamespaceDir names the namespace directory when no namespace is set.
	snapshotTokenNamespaceDir = "_token_namespace"
	// secretRefMarker marks unresolved secret references in config content.
	secretRefMarker = "${secret:"
	// maxSnapshotBytes bounds the size of a snapshot file that is read back.
	maxSnapshotBytes = 32 << 20
	snapshotDirMode  = 0o700
	snapshotFileMode = 0o600
)

// snapshotDocument is the on-disk representation of one config item.
type snapshotDocument struct {
	Format    int             `json:"format"`
	SavedAt   string          `json:"saved_at"`
	Encrypted bool            `json:"encrypted"`
	Item      json.RawMessage `json:"item,omitempty"`
}

// snapshotStore keeps the latest version of each watched item under
// <root>/<host>/<namespace>/<group>/<key>.json (every component
// percent-encoded), written atomically with mode 0600 in 0700 directories.
// Secret items are never written: their older snapshots are removed instead.
type snapshotStore struct {
	root          string
	dir           string // relative to root: <host>/<namespace>
	treatAsSecret func(*ConfigItem) bool
	logger        *slog.Logger
}

func newSnapshotStore(root, host, namespace string, treatAsSecret func(*ConfigItem) bool, logger *slog.Logger) *snapshotStore {
	if namespace == "" {
		namespace = snapshotTokenNamespaceDir
	}
	return &snapshotStore{
		root:          root,
		dir:           path.Join(snapshotComponent(host), snapshotComponent(namespace)),
		treatAsSecret: treatAsSecret,
		logger:        logger,
	}
}

// snapshotComponent encodes an arbitrary string as one safe path component:
// every byte except ASCII letters, digits and "_.-~" is percent-encoded, a
// leading dot is encoded, and the empty string becomes "%00".
func snapshotComponent(name string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		unreserved := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '_' || c == '.' || c == '-' || c == '~'
		if unreserved && (c != '.' || i > 0) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	if b.Len() == 0 {
		return "%00"
	}
	return b.String()
}

// relPath returns the snapshot file of an item relative to the root.
func (s *snapshotStore) relPath(group, key string) string {
	return path.Join(s.dir, snapshotComponent(group), snapshotComponent(key)+".json")
}

// isSecret reports whether an item must not be persisted. A predicate that
// panics marks the item as secret.
func (s *snapshotStore) isSecret(item *ConfigItem) (secret bool) {
	if item.GetHasSecretRefs() || strings.Contains(item.GetContent(), secretRefMarker) {
		return true
	}
	if s.treatAsSecret == nil {
		return false
	}
	defer func() {
		if p := recover(); p != nil {
			s.logger.Error("spinneret TreatAsSecret panicked, treating the item as secret",
				slog.String("group", item.GetGroup()), slog.String("key", item.GetKey()))
			secret = true
		}
	}()
	return s.treatAsSecret(item)
}

func (s *snapshotStore) openRoot() (*os.Root, error) {
	if err := os.MkdirAll(s.root, snapshotDirMode); err != nil {
		return nil, err
	}
	return os.OpenRoot(s.root)
}

// save writes the snapshot of item and reports whether a file was written
// (false for secret items, whose older snapshot is removed).
func (s *snapshotStore) save(item *ConfigItem) (bool, error) {
	root, err := s.openRoot()
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()
	rel := s.relPath(item.GetGroup(), item.GetKey())
	if s.isSecret(item) {
		if err := root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		return false, nil
	}
	encoded, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(item)
	if err != nil {
		return false, err
	}
	data, err := json.Marshal(snapshotDocument{
		Format:  snapshotFormat,
		SavedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000000Z"),
		Item:    encoded,
	})
	if err != nil {
		return false, err
	}
	if err := atomicWrite(root, rel, data); err != nil {
		return false, err
	}
	return true, nil
}

// atomicWrite writes data to a temporary file next to rel, syncs it, renames
// it over rel and syncs the directory.
func atomicWrite(root *os.Root, rel string, data []byte) error {
	dir := path.Dir(rel)
	if err := root.MkdirAll(dir, snapshotDirMode); err != nil {
		return err
	}
	tmp := path.Join(dir, "."+path.Base(rel)+"."+NewReportID()+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, snapshotFileMode)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = root.Rename(tmp, rel)
	}
	if werr != nil {
		_ = root.Remove(tmp)
		return werr
	}
	if d, err := root.Open(dir); err == nil {
		_ = d.Sync() // best effort: not every platform can sync directories
		_ = d.Close()
	}
	return nil
}

// load reads the snapshot of an item; (nil, nil) when there is none or it
// must be ignored (encrypted by another SDK, flagged as secret).
func (s *snapshotStore) load(group, key string) (*ConfigItem, error) {
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(s.relPath(group, key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSnapshotBytes {
		return nil, errors.New("snapshot file too large")
	}
	var doc snapshotDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}
	if doc.Format != snapshotFormat {
		return nil, fmt.Errorf("unsupported snapshot format %d", doc.Format)
	}
	if doc.Encrypted || len(doc.Item) == 0 {
		return nil, nil
	}
	item := new(ConfigItem)
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(doc.Item, item); err != nil {
		return nil, fmt.Errorf("decode snapshot item: %w", err)
	}
	if item.GetGroup() != group || item.GetKey() != key {
		return nil, errors.New("snapshot belongs to another item")
	}
	if s.isSecret(item) {
		return nil, nil
	}
	return item, nil
}
