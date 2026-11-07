package registry

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Hasher turns a password into the stored secret and checks it later.
type Hasher interface {
	Hash(password string) (string, error)
	Verify(password, encoded string) bool
}

// Argon2id is the password hasher. Memory is in KiB, matching argon2.IDKey.
type Argon2id struct {
	Time        uint32
	Memory      uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
	Random      io.Reader
}

// DefaultArgon2id uses the OWASP-style 64 MiB, three iterations, two lanes.
func DefaultArgon2id() *Argon2id {
	return NewArgon2id(3, 64*1024, 2)
}

// NewArgon2id builds a hasher. Tests pass small memory so concurrent tests do
// not allocate gigabytes.
func NewArgon2id(iterations, memory uint32, parallelism uint8) *Argon2id {
	return &Argon2id{
		Time:        iterations,
		Memory:      memory,
		Parallelism: parallelism,
		SaltLength:  16,
		KeyLength:   32,
		Random:      rand.Reader,
	}
}

// Hash returns the PHC string stored in logins.secret_hash.
func (h *Argon2id) Hash(password string) (string, error) {
	time, memory, parallelism, saltLen, keyLen, randSource := h.params()
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(randSource, salt); err != nil {
		return "", fmt.Errorf("argon2 salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, time, memory, parallelism, keyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		memory,
		time,
		parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify reports whether password matches encoded. A malformed hash is a miss,
// never an error, so a damaged row cannot lock the process.
func (h *Argon2id) Verify(password, encoded string) bool {
	params, salt, want, err := parseArgon2id(encoded)
	if err != nil {
		return false
	}
	got := argon2.IDKey(
		[]byte(password),
		salt,
		params.time,
		params.memory,
		params.parallelism,
		uint32(len(want)),
	)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (h *Argon2id) params() (uint32, uint32, uint8, uint32, uint32, io.Reader) {
	time := h.Time
	if time == 0 {
		time = 3
	}
	memory := h.Memory
	if memory == 0 {
		memory = 64 * 1024
	}
	parallelism := h.Parallelism
	if parallelism == 0 {
		parallelism = 2
	}
	saltLen := h.SaltLength
	if saltLen == 0 {
		saltLen = 16
	}
	keyLen := h.KeyLength
	if keyLen == 0 {
		keyLen = 32
	}
	source := h.Random
	if source == nil {
		source = rand.Reader
	}
	return time, memory, parallelism, saltLen, keyLen, source
}

type argonParams struct {
	time        uint32
	memory      uint32
	parallelism uint8
}

var errMalformedHash = errors.New("registry: malformed argon2id hash")

func parseArgon2id(encoded string) (argonParams, []byte, []byte, error) {
	// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<key>
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return argonParams{}, nil, nil, errMalformedHash
	}
	version, err := parseKV(parts[2], "v")
	if err != nil || version != argon2.Version {
		return argonParams{}, nil, nil, errMalformedHash
	}
	values := strings.Split(parts[3], ",")
	if len(values) != 3 {
		return argonParams{}, nil, nil, errMalformedHash
	}
	mem, err := parseKV(values[0], "m")
	if err != nil {
		return argonParams{}, nil, nil, errMalformedHash
	}
	time, err := parseKV(values[1], "t")
	if err != nil {
		return argonParams{}, nil, nil, errMalformedHash
	}
	parallelism, err := parseKV(values[2], "p")
	if err != nil {
		return argonParams{}, nil, nil, errMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return argonParams{}, nil, nil, errMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return argonParams{}, nil, nil, errMalformedHash
	}
	if mem == 0 || time == 0 || parallelism == 0 {
		return argonParams{}, nil, nil, errMalformedHash
	}
	return argonParams{
		time:        uint32(time),
		memory:      uint32(mem),
		parallelism: uint8(parallelism),
	}, salt, key, nil
}

func parseKV(value, key string) (int64, error) {
	prefix := key + "="
	if !strings.HasPrefix(value, prefix) {
		return 0, errMalformedHash
	}
	return strconv.ParseInt(strings.TrimPrefix(value, prefix), 10, 64)
}
