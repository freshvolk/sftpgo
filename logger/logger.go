// Package logger provides logging capabilities.
// It is a wrapper around zerolog for logging and lumberjack for log rotation.
// Logs are written to the specified log file.
// Logging on the console is provided to print initialization info, errors and warnings.
// The package provides a request logger to log the HTTP requests for REST API too.
// The request logger uses chi.middleware.RequestLogger,
// chi.middleware.LogFormatter and chi.middleware.LogEntry to build a structured
// logger using zerolog
package logger

import (
	"fmt"
	"os"
	"runtime"
	"sync"

	"github.com/rs/zerolog"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

const (
	dateFormat = "2006-01-02T15:04:05.000" // YYYY-MM-DDTHH:MM:SS.ZZZ
)

// LogLevel defines log levels.
type LogLevel uint8

// defines our own log level, just in case we'll change logger in future
const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarn
	LevelError
)

var (
	logger        zerolog.Logger
	consoleLogger zerolog.Logger
)

// GetLogger get the configured logger instance
func GetLogger() *zerolog.Logger {
	return &logger
}

// InitLogger configures the logger using the given parameters
func InitLogger(logFilePath string, logMaxSize int, logMaxBackups int, logMaxAge int, logCompress bool, level zerolog.Level) {
	zerolog.TimeFieldFormat = dateFormat
	if len(logFilePath) > 0 {
		logger = zerolog.New(&lumberjack.Logger{
			Filename:   logFilePath,
			MaxSize:    logMaxSize,
			MaxBackups: logMaxBackups,
			MaxAge:     logMaxAge,
			Compress:   logCompress,
		})
		EnableConsoleLogger(level)
	} else {
		logger = zerolog.New(logSyncWrapper{
			output: os.Stdout,
			lock:   new(sync.Mutex)})
		consoleLogger = zerolog.Nop()
	}
	logger.Level(level)
}

// InitCustomLogger configures the logger using a passed in zerolog logger
func InitCustomLogger(customLogger zerolog.Logger) {
	zerolog.TimeFieldFormat = dateFormat
	logger = customLogger
	consoleLogger = zerolog.Nop()
}

// DisableLogger disable the main logger.
// ConsoleLogger will not be affected
func DisableLogger() {
	logger = zerolog.Nop()
}

// EnableConsoleLogger enables the console logger
func EnableConsoleLogger(level zerolog.Level) {
	consoleOutput := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: dateFormat,
		NoColor:    runtime.GOOS == "windows",
	}
	consoleLogger = zerolog.New(consoleOutput).With().Timestamp().Logger().Level(level)
}

// Log logs at the specified level for the specified sender
func Log(level LogLevel, sender string, connectionID string, format string, v ...interface{}) {
	switch level {
	case LevelDebug:
		Debug(sender, connectionID, format, v...)
	case LevelInfo:
		Info(sender, connectionID, format, v...)
	case LevelWarn:
		Warn(sender, connectionID, format, v...)
	default:
		Error(sender, connectionID, format, v...)
	}

}

// Debug logs at debug level for the specified sender
func Debug(sender string, connectionID string, format string, v ...interface{}) {
	logger.Debug().Timestamp().Str("sender", sender).Str("connection_id", connectionID).Msg(fmt.Sprintf(format, v...))
}

// Info logs at info level for the specified sender
func Info(sender string, connectionID string, format string, v ...interface{}) {
	logger.Info().Timestamp().Str("sender", sender).Str("connection_id", connectionID).Msg(fmt.Sprintf(format, v...))
}

// Warn logs at warn level for the specified sender
func Warn(sender string, connectionID string, format string, v ...interface{}) {
	logger.Warn().Timestamp().Str("sender", sender).Str("connection_id", connectionID).Msg(fmt.Sprintf(format, v...))
}

// Error logs at error level for the specified sender
func Error(sender string, connectionID string, format string, v ...interface{}) {
	logger.Error().Timestamp().Str("sender", sender).Str("connection_id", connectionID).Msg(fmt.Sprintf(format, v...))
}

// DebugToConsole logs at debug level to stdout
func DebugToConsole(format string, v ...interface{}) {
	consoleLogger.Debug().Msg(fmt.Sprintf(format, v...))
}

// InfoToConsole logs at info level to stdout
func InfoToConsole(format string, v ...interface{}) {
	consoleLogger.Info().Msg(fmt.Sprintf(format, v...))
}

// WarnToConsole logs at info level to stdout
func WarnToConsole(format string, v ...interface{}) {
	consoleLogger.Warn().Msg(fmt.Sprintf(format, v...))
}

// ErrorToConsole logs at error level to stdout
func ErrorToConsole(format string, v ...interface{}) {
	consoleLogger.Error().Msg(fmt.Sprintf(format, v...))
}

// TransferLog logs an SFTP/SCP upload or download
func TransferLog(operation string, path string, elapsed int64, size int64, user string, connectionID string, protocol string) {
	logger.Info().
		Timestamp().
		Str("sender", operation).
		Int64("elapsed_ms", elapsed).
		Int64("size_bytes", size).
		Str("username", user).
		Str("file_path", path).
		Str("connection_id", connectionID).
		Str("protocol", protocol).
		Msg("")
}

// CommandLog logs an SFTP/SCP/SSH command
func CommandLog(command, path, target, user, fileMode, connectionID, protocol string, uid, gid int, atime, mtime, sshCommand string) {
	logger.Info().
		Timestamp().
		Str("sender", command).
		Str("username", user).
		Str("file_path", path).
		Str("target_path", target).
		Str("filemode", fileMode).
		Int("uid", uid).
		Int("gid", gid).
		Str("access_time", atime).
		Str("modification_time", atime).
		Str("ssh_command", sshCommand).
		Str("connection_id", connectionID).
		Str("protocol", protocol).
		Msg("")
}

// ConnectionFailedLog logs failed attempts to initialize a connection.
// A connection can fail for an authentication error or other errors such as
// a client abort or a time out if the login does not happen in two minutes.
// These logs are useful for better integration with Fail2ban and similar tools.
func ConnectionFailedLog(user, ip, loginType, errorString string) {
	logger.Debug().
		Timestamp().
		Str("sender", "connection_failed").
		Str("client_ip", ip).
		Str("username", user).
		Str("login_type", loginType).
		Str("error", errorString).
		Msg("")
}
