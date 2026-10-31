package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/dochevviktor/server-fan-control/internal/config"
)

type fanKey struct {
	UUID  string
	Index int
}
type journalDocument struct {
	SchemaVersion int              `json:"schema_version"`
	Fans          map[string][]int `json:"fans"`
}

func readJournal(path string) (map[fanKey]bool, error) {
	owned := map[fanKey]bool{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return owned, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 1024*1024 {
		return nil, errors.New("ownership journal exceeds 1 MiB")
	}
	var document journalDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("invalid ownership journal: %w", err)
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("ownership journal must contain one JSON document")
	}
	if document.SchemaVersion != 1 || document.Fans == nil {
		return nil, errors.New("invalid ownership journal schema")
	}
	for uuid, fans := range document.Fans {
		if !strings.HasPrefix(uuid, "GPU-") || len(uuid) > 100 || strings.ContainsAny(uuid, "/\\ \r\n\t") {
			return nil, errors.New("invalid journal GPU UUID")
		}
		for _, index := range fans {
			key := fanKey{uuid, index}
			if index < 0 || index > 63 || owned[key] {
				return nil, errors.New("invalid journal fan index")
			}
			owned[key] = true
		}
	}
	return owned, nil
}
func writeJournal(path string, owned map[fanKey]bool) error {
	document := journalDocument{SchemaVersion: 1, Fans: map[string][]int{}}
	for key := range owned {
		document.Fans[key.UUID] = append(document.Fans[key.UUID], key.Index)
	}
	for _, fans := range document.Fans {
		sort.Ints(fans)
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return config.AtomicWrite(path, append(data, '\n'))
}
func copyOwnership(in map[fanKey]bool) map[fanKey]bool {
	out := make(map[fanKey]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
