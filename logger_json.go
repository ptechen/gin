package gin

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/ptechen/encoding"
	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/diode"
	"github.com/rs/zerolog/log"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// TraceLevel defines trace log level.
	TraceLevel = iota - 1
	// DebugLevel defines debug log level.
	DebugLevel
	// InfoLevel defines info log level.
	InfoLevel
	// WarnLevel defines warn log level.
	WarnLevel
	// ErrorLevel defines error log level.
	ErrorLevel
	// FatalLevel defines fatal log level.
	FatalLevel
	// PanicLevel defines panic log level.
	PanicLevel
	// NoLevel defines an absent log level.
	NoLevel
	// Disabled disables the logger.
	Disabled
)

const (
	// TimeFormatUnix defines a time format that makes time fields to be
	// serialized as Unix timestamp integers.
	TimeFormatUnix = ""

	// TimeFormatUnixMs defines a time format that makes time fields to be
	// serialized as Unix timestamp integers in milliseconds.
	TimeFormatUnixMs = "UNIXMS"

	// TimeFormatUnixMicro defines a time format that makes time fields to be
	// serialized as Unix timestamp integers in microseconds.
	TimeFormatUnixMicro = "UNIXMICRO"
)

// JsonLoggerConfig defines the config for Logger middleware.
type JsonLoggerConfig struct {
	// Output is a writer where logs are written.
	// Optional. Default value is gin.DefaultWriter.
	Output io.Writer

	// SkipPaths is a url path array which logs are not written.
	// Optional.
	SkipPaths []string

	// IsConsole is whether to enable terminal printing.
	IsConsole bool

	// LogLevel is log level.
	LogLevel int8

	// Caller is whether to enable log tracking.
	Caller bool

	// LogColor is whether to enable log color.
	LogColor bool

	// LogWriteSize is sets the size of the log write pipeline.
	LogWriteSize int

	// LogTimeFieldFormat is a formatted layout of time fields in the log.
	LogTimeFieldFormat string

	// LogExpDays is number of days the log is kept.
	LogExpDays int64

	// LogLimitSize is the limit size of the log file, for example 1G and 512MB.
	LogLimitSize string

	logFilePath string

	logDir string

	logName string

	logLimitNums int64
}

// JsonLogger instances a Logger middleware that will write the logs to gin.DefaultWriter.
// By default gin.DefaultWriter = os.Stdout.
func JsonLogger() HandlerFunc {
	return JsonLoggerWithConfig(JsonLoggerConfig{
		LogColor:  true,
		IsConsole: true,
	})
}

var once sync.Once
var logger *zerolog.Logger
var skip map[string]struct{}

type TraceParams struct {
	StartTime time.Time
	Path      string
	ClientIp  string
	Method    string
}

// JsonLoggerWithWriter instance a Logger middleware with the specified writer buffer.
// Example: os.Stdout, a file opened in write mode, a socket...
func JsonLoggerWithWriter(conf JsonLoggerConfig) HandlerFunc {
	return JsonLoggerWithConfig(conf)
}

// JsonLoggerWithConfig instance a Logger middleware with config.
func JsonLoggerWithConfig(conf JsonLoggerConfig) HandlerFunc {
	if conf.Output == nil {
		conf.Output = DefaultWriter
	}

	once.Do(func() {
		conf.InitLogConfig()

		conf.Monitor()

		notLogged := conf.SkipPaths
		length := len(notLogged)
		if length > 0 {
			skip = make(map[string]struct{}, length)
			for i := 0; i < length; i++ {
				skip[notLogged[i]] = struct{}{}
			}
		}
	})

	return func(c *Context) {
		// Start timer
		start := time.Now()
		path := c.Request.URL.Path

		params := &TraceParams{
			StartTime: start,
			Path:      c.Request.URL.String(),
			ClientIp:  c.ClientIP(),
			Method:    c.Request.Method,
		}

		traceId, _ := createUuid(params)
		c.Logger = log.With().
			Str("trace_id", traceId).
			Str("path", c.Request.URL.String()).
			Str("client_ip", c.ClientIP()).
			Str("method", c.Request.Method).
			Int("body_size", c.Writer.Size()).
			Logger()

		// Process request
		c.Next()

		// Log only when path is not being skipped
		if _, ok := skip[path]; !ok {
			param := LogFormatterParams{
				Request: c.Request,
				//isTerm:  isTerm,
				Keys: c.Keys,
			}

			// Stop timer
			param.TimeStamp = time.Now()
			param.Latency = param.TimeStamp.Sub(start)
			param.StatusCode = c.Writer.Status()
			param.ErrorMessage = c.Errors.ByType(ErrorTypePrivate).String()
			if param.ErrorMessage == "" {
				c.Logger.Info().Dur("latency", param.Latency).
					Int("status", param.StatusCode).
					Interface("keys", c.Keys).Send()
			} else {
				c.Logger.Err(errors.New(param.ErrorMessage)).
					Dur("latency", param.Latency).
					Int("status", param.StatusCode).
					Interface("keys", c.Keys).Send()
			}
		}
	}
}

// InitLogConfig is the method to initialize the log configuration.
func (p *JsonLoggerConfig) InitLogConfig() {
	logger = &log.Logger
	zerolog.TimeFieldFormat = p.LogTimeFieldFormat
	p.SetFilePath2FileName()
	p.SetLogFileSize()
	p.SetLoglevel()
	p.CheckLogExpDays()
	p.setCaller()
	p.CheckLogWriteSize()
	p.SetOutput()
}

func (p *JsonLoggerConfig) setCaller() {
	if p.Caller {
		*logger = logger.With().Caller().Logger()
	}
}

func (p *JsonLoggerConfig) ReCreateLogFile() {
	f, _ := os.OpenFile(p.logFilePath, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0666)
	p.Output = f
}

// SetOutput is a method to set the log output path.
func (p *JsonLoggerConfig) SetOutput() {
	if p.IsConsole && p.LogColor {
		p.Output = zerolog.ConsoleWriter{Out: p.Output}
	}

	w := diode.NewWriter(p.Output, p.LogWriteSize, 10*time.Millisecond, func(missed int) {
		logger.Warn().Msgf("Logger Dropped %d messages", missed)
	})

	*logger = logger.Output(w)
}

// SetLoglevel is a method to set the alarm level for checking logs.
func (p *JsonLoggerConfig) SetLoglevel() {
	if p.LogLevel < -1 || p.LogLevel > 7 {
		p.LogLevel = 0
	}
	*logger = logger.Level(zerolog.Level(p.LogLevel))
}

// CheckLogWriteSize is a method to set the default log write channel size.
func (p *JsonLoggerConfig) CheckLogWriteSize() {
	if p.LogWriteSize < 1000 {
		p.LogWriteSize = 1000
	}
}

// SetLogFileSize is a method for setting a limit on the size of a log file.
func (p *JsonLoggerConfig) SetLogFileSize() {
	if !strings.Contains(p.LogLimitSize, "G") &&
		!strings.Contains(p.LogLimitSize, "MB") &&
		!strings.Contains(p.LogLimitSize, "KB") {
		p.LogLimitSize = "1G"
	}

	if strings.Contains(p.LogLimitSize, "G") {
		n, _ := strconv.Atoi(strings.Split(p.LogLimitSize, "G")[0])
		p.logLimitNums = int64(n) * 1024 * 1024 * 1024
	} else if strings.Contains(p.LogLimitSize, "KB") {
		n, _ := strconv.Atoi(strings.Split(p.LogLimitSize, "MB")[0])
		p.logLimitNums = int64(n) * 1024 * 1024
	} else {
		n, _ := strconv.Atoi(strings.Split(p.LogLimitSize, "KB")[0])
		p.logLimitNums = int64(n) * 1024
	}
}

// CheckLogExpDays is a method to check if the log file has an expiration time set.
func (p *JsonLoggerConfig) CheckLogExpDays() {
	if p.LogExpDays == 0 {
		p.LogExpDays = 30
	}
}

// SetFilePath2FileName is a method for the path and name of the log file.
func (p *JsonLoggerConfig) SetFilePath2FileName() {
	data, ok := p.Output.(*os.File)
	if ok && !p.IsConsole {
		p.logFilePath = data.Name()
		p.logDir, p.logName = parseFileInfo(p.logFilePath)
	}
}

func parseFileInfo(fileInfo string) (logDir, logName string) {
	if strings.Contains(fileInfo, "/") {
		fileInfo := strings.SplitAfter(fileInfo, "/")
		length := len(fileInfo) - 1
		logDir = strings.Join(fileInfo[:length], "")
		logName = fileInfo[length]
	} else {
		logDir, logName = "./", fileInfo
	}
	return
}

func (p *JsonLoggerConfig) tab1() {
	logger.Info().Msg("tab1")
	isExist := p.IsExist()
	if !isExist {
		p.SetOutput()
	}
	size := p.CheckFileSize()
	if size > p.logLimitNums {
		p.Rename2File()
		p.ReCreateLogFile()
		p.SetOutput()
	}
}

// Monitor is a method of monitoring log files.
func (p *JsonLoggerConfig) Monitor() {
	if p.logFilePath == "" || p.logName == "" {
		return
	}
	cronTab := cron.New()
	cronTab.AddFunc("*/5 * * * * ?", p.tab1)
	cronTab.AddFunc("0 0 1 * * ?", p.DeleteLogFile)
	cronTab.Start()
}

// IsExist is a method to check if the log file exists.
func (p *JsonLoggerConfig) IsExist() bool {
	_, err := os.Stat(p.logFilePath)
	return err == nil || os.IsExist(err)
}

// CheckFileSize is a method for checking the size of a log file.
func (p *JsonLoggerConfig) CheckFileSize() int64 {
	f, e := os.Stat(p.logFilePath)
	if e != nil {
		return 0
	}
	return f.Size()
}

// Rename2File is a method for renaming log files.
func (p *JsonLoggerConfig) Rename2File() (newLogFileName string) {
	now := time.Now()
	newLogFileName = fmt.Sprintf("%s.%s", p.logFilePath, now.Format("2006-01-02 15:04:05"))
	err := os.Rename(p.logFilePath, newLogFileName)
	if err != nil {
		return ""
	}
	return
}

// DeleteLogFile is a method for deleting log files.
func (p *JsonLoggerConfig) DeleteLogFile() {
	logger.Info().Msg("tab1")
	files, _ := ioutil.ReadDir(p.logDir)
	for _, file := range files {
		if !file.IsDir() {
			if file.Name() != p.logName && strings.Contains(file.Name(), p.logName) {
				createTime := strings.Split(file.Name(), p.logName+".")[1]
				date, err := time.Parse("2006-01-02 15:04:05", createTime)
				if err != nil {
					continue
				}
				dateUnix := date.Unix()
				currentUnix := time.Now().Unix()
				if currentUnix-dateUnix > p.LogExpDays*60*60*24 {
					currentFileName := p.logDir + file.Name()
					_ = os.Remove(currentFileName)
				}
			}
		}
	}
}

// CreateUuid is the method used to generate the tracking id.
func createUuid(params interface{}) (uuidStr string, err error) {
	data, err := encoding.JSON.Marshal(params)
	if err != nil {
		return uuidStr, err
	}
	uuidStr = uuid.NewMD5(uuid.UUID{}, data).String()
	return uuidStr, err
}
