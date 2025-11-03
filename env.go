package mono

import (
	"bufio"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

var (
	EnvMonoEnv        = "MONO_ENV"
	EnvMonoRps        = "MONO_RPS"
	EnvMonoRpsClients = "MONO_RPS_CLIENTS"

	CurrentEnv                Environment
	InMemoryFilesizeThreshold int64 = 1 << 20
	TempDir                         = "" // "" <=> default OS' temp dir
	TempDirClean                    = true
	EnableTLS                       = EnableTLSUnspecified
	Log                             = slog.Default()

	Filetypes = map[string][]string{
		"img":   {".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tiff", ".heic"},
		"video": {".mp4", ".mov", ".webm", ".heiv"},
		"audio": {".mp3", ".wav", ".flac", ".ogg", ".aac"},
	}
	FiletypesTags = map[string]string{
		"img":   `<img src="%s" alt="%s">`,
		"video": `<video src="%s" alt="%s" preload="metadata" loop autoplay muted controls>Does you browser support videos?</video>`,
		"audio": `<audio src="%s" alt="%s" onloadedmetadata="this.volume=0.25" controls>Does your Linux support audio?</audio>`,
	}

	DefaultPageDynamicFuncs = template.FuncMap{
		"mono_time": func() string { return time.Now().String() },
	}
)

func init() {
	if CurrentEnv == envUnspecified {
		switch strings.ToLower(os.Getenv(EnvMonoEnv)) {
		case "":
			if isDocker() {
				CurrentEnv = EnvProd
				break
			}
			fallthrough
		case "local":
			CurrentEnv = EnvLocal
		case "dev":
			CurrentEnv = EnvDev
		case "prod":
			CurrentEnv = EnvProd
		default:
			panic(fmt.Sprintf("unknown environment variable MONO_ENV: %s", os.Getenv("MONO_ENV")))
		}
	}
}

type Environment int64

const (
	envUnspecified Environment = iota
	EnvLocal
	EnvDev
	EnvProd
)
const (
	EnableTLSUnspecified = iota
	EnableTLSTrue        = 1
	EnableTLSFalse       = 2
)

func IsLocal() bool { return CurrentEnv == EnvLocal }
func IsDev() bool   { return CurrentEnv == EnvDev }
func IsProd() bool  { return CurrentEnv == EnvProd }

var statusMessageCache = [600][]byte{}

func responseError(rw http.ResponseWriter, status int) error {
	if len(statusMessageCache[status]) == 0 {
		statusMessageCache[status] = []byte(fmt.Sprintf("%d %s", status, http.StatusText(status)))
	}

	rw.WriteHeader(status)
	if _, err := rw.Write(statusMessageCache[status]); err != nil {
		return err
	}
	return nil
}

func enableTLS() bool {
	if EnableTLS != EnableTLSUnspecified {
		return EnableTLS == EnableTLSTrue
	}
	return !IsLocal()
}

func isDocker() bool {
	file, err := os.Open("/proc/1/cgroup")
	if err != nil {
		return false
	}
	defer func(file *os.File) { _ = file.Close() }(file)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "docker") || strings.Contains(line, "containerd") {
			return true
		}
	}
	return false
}
