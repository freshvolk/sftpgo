// Package dataprovider provides data access.
// It abstract different data providers and exposes a common API.
// Currently the supported data providers are: PostreSQL (9+), MySQL (4.1+) and SQLite 3.x
package dataprovider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexedwards/argon2id"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/crypto/ssh"

	"github.com/freshvolk/sftpgo/logger"
	"github.com/freshvolk/sftpgo/metrics"
	"github.com/freshvolk/sftpgo/utils"
	"github.com/freshvolk/sftpgo/vfs"
	unixcrypt "github.com/nathanaelle/password"
)

const (
	// SQLiteDataProviderName name for SQLite database provider
	SQLiteDataProviderName = "sqlite"
	// PGSQLDataProviderName name for PostgreSQL database provider
	PGSQLDataProviderName = "postgresql"
	// MySQLDataProviderName name for MySQL database provider
	MySQLDataProviderName = "mysql"
	// BoltDataProviderName name for bbolt key/value store provider
	BoltDataProviderName = "bolt"
	// MemoryDataProviderName name for memory provider
	MemoryDataProviderName = "memory"

	argonPwdPrefix           = "$argon2id$"
	bcryptPwdPrefix          = "$2a$"
	pbkdf2SHA1Prefix         = "$pbkdf2-sha1$"
	pbkdf2SHA256Prefix       = "$pbkdf2-sha256$"
	pbkdf2SHA512Prefix       = "$pbkdf2-sha512$"
	md5cryptPwdPrefix        = "$1$"
	md5cryptApr1PwdPrefix    = "$apr1$"
	sha512cryptPwdPrefix     = "$6$"
	manageUsersDisabledError = "please set manage_users to 1 in your configuration to enable this method"
	trackQuotaDisabledError  = "please enable track_quota in your configuration to use this method"
	operationAdd             = "add"
	operationUpdate          = "update"
	operationDelete          = "delete"
)

var (
	// SupportedProviders data provider configured in the sftpgo.conf file must match of these strings
	SupportedProviders = []string{SQLiteDataProviderName, PGSQLDataProviderName, MySQLDataProviderName,
		BoltDataProviderName, MemoryDataProviderName}
	// ValidPerms list that contains all the valid permissions for an user
	ValidPerms = []string{PermAny, PermListItems, PermDownload, PermUpload, PermOverwrite, PermRename, PermDelete,
		PermCreateDirs, PermCreateSymlinks, PermChmod, PermChown, PermChtimes}
	config          Config
	provider        Provider
	sqlPlaceholders []string
	hashPwdPrefixes = []string{argonPwdPrefix, bcryptPwdPrefix, pbkdf2SHA1Prefix, pbkdf2SHA256Prefix,
		pbkdf2SHA512Prefix, md5cryptPwdPrefix, md5cryptApr1PwdPrefix, sha512cryptPwdPrefix}
	pbkdfPwdPrefixes       = []string{pbkdf2SHA1Prefix, pbkdf2SHA256Prefix, pbkdf2SHA512Prefix}
	unixPwdPrefixes        = []string{md5cryptPwdPrefix, md5cryptApr1PwdPrefix, sha512cryptPwdPrefix}
	logSender              = "dataProvider"
	availabilityTicker     *time.Ticker
	availabilityTickerDone chan bool
	errWrongPassword       = errors.New("password does not match")
	credentialsDirPath     string
)

// Actions to execute on user create, update, delete.
// An external command can be executed and/or an HTTP notification can be fired
type Actions struct {
	// Valid values are add, update, delete. Empty slice to disable
	ExecuteOn []string `json:"execute_on" mapstructure:"execute_on"`
	// Absolute path to the command to execute, empty to disable
	Command string `json:"command" mapstructure:"command"`
	// The URL to notify using an HTTP POST.
	// The action is added to the query string. For example <url>?action=update.
	// The user is sent serialized as json inside the POST body.
	// Empty to disable
	HTTPNotificationURL string `json:"http_notification_url" mapstructure:"http_notification_url"`
}

// Config provider configuration
type Config struct {
	// Driver name, must be one of the SupportedProviders
	Driver string `json:"driver" mapstructure:"driver"`
	// Database name. For driver sqlite this can be the database name relative to the config dir
	// or the absolute path to the SQLite database.
	Name string `json:"name" mapstructure:"name"`
	// Database host
	Host string `json:"host" mapstructure:"host"`
	// Database port
	Port int `json:"port" mapstructure:"port"`
	// Database username
	Username string `json:"username" mapstructure:"username"`
	// Database password
	Password string `json:"password" mapstructure:"password"`
	// Used for drivers mysql and postgresql.
	// 0 disable SSL/TLS connections.
	// 1 require ssl.
	// 2 set ssl mode to verify-ca for driver postgresql and skip-verify for driver mysql.
	// 3 set ssl mode to verify-full for driver postgresql and preferred for driver mysql.
	SSLMode int `json:"sslmode" mapstructure:"sslmode"`
	// Custom database connection string.
	// If not empty this connection string will be used instead of build one using the previous parameters
	ConnectionString string `json:"connection_string" mapstructure:"connection_string"`
	// Database table for SFTP users
	UsersTable string `json:"users_table" mapstructure:"users_table"`
	// Set to 0 to disable users management, 1 to enable
	ManageUsers int `json:"manage_users" mapstructure:"manage_users"`
	// Set the preferred way to track users quota between the following choices:
	// 0, disable quota tracking. REST API to scan user dir and update quota will do nothing
	// 1, quota is updated each time a user upload or delete a file even if the user has no quota restrictions
	// 2, quota is updated each time a user upload or delete a file but only for users with quota restrictions.
	//    With this configuration the "quota scan" REST API can still be used to periodically update space usage
	//    for users without quota restrictions
	TrackQuota int `json:"track_quota" mapstructure:"track_quota"`
	// Sets the maximum number of open connections for mysql and postgresql driver.
	// Default 0 (unlimited)
	PoolSize int `json:"pool_size" mapstructure:"pool_size"`
	// Users' default base directory.
	// If no home dir is defined while adding a new user, and this value is
	// a valid absolute path, then the user home dir will be automatically
	// defined as the path obtained joining the base dir and the username
	UsersBaseDir string `json:"users_base_dir" mapstructure:"users_base_dir"`
	// Actions to execute on user add, update, delete.
	// Update action will not be fired for internal updates such as the last login or the user quota fields.
	Actions Actions `json:"actions" mapstructure:"actions"`
	// Absolute path to an external program to use for users authentication. Leave empty to use builtin
	// authentication.
	// The external program can read the following environment variables to get info about the user trying
	// to authenticate:
	//
	// - SFTPGO_AUTHD_USERNAME
	// - SFTPGO_AUTHD_PASSWORD, not empty for password authentication
	// - SFTPGO_AUTHD_PUBLIC_KEY, not empty for public key authentication
	//
	// The content of these variables is _not_ quoted. They may contain special characters. They are under the
	// control of a possibly malicious remote user.
	//
	// The program must respond on the standard output with a valid SFTPGo user serialized as json if the
	// authentication succeed or an user with an empty username if the authentication fails.
	// If the authentication succeed the user will be automatically added/updated inside the defined data provider.
	// Actions defined for user added/updated will not be executed in this case.
	// The external program should check authentication only, if there are login restrictions such as user
	// disabled, expired, login allowed only from specific IP addresses it is enough to populate the matching user
	// fields and these conditions will be checked in the same way as for builtin users.
	// The external auth program must finish within 15 seconds.
	// This method is slower than built-in authentication methods, but it's very flexible as anyone can
	// easily write his own authentication programs.
	ExternalAuthProgram string `json:"external_auth_program" mapstructure:"external_auth_program"`
	// ExternalAuthScope defines the scope for the external authentication program.
	// - 0 means all supported authetication scopes, the external program will be used for password,
	//     public key and keyboard interactive authentication
	// - 1 means passwords only
	// - 2 means public keys only
	// - 4 means keyboard interactive only
	// you can combine the scopes, for example 3 means password and public key, 5 password and keyboard
	// interactive and so on
	ExternalAuthScope int `json:"external_auth_scope" mapstructure:"external_auth_scope"`
	// CredentialsPath defines the directory for storing user provided credential files such as
	// Google Cloud Storage credentials. It can be a path relative to the config dir or an
	// absolute path
	CredentialsPath string `json:"credentials_path" mapstructure:"credentials_path"`
}

// BackupData defines the structure for the backup/restore files
type BackupData struct {
	Users []User `json:"users"`
}

type keyboardAuthProgramResponse struct {
	Instruction string   `json:"instruction"`
	Questions   []string `json:"questions"`
	Echos       []bool   `json:"echos"`
	AuthResult  int      `json:"auth_result"`
}

// ValidationError raised if input data is not valid
type ValidationError struct {
	err string
}

// Validation error details
func (e *ValidationError) Error() string {
	return fmt.Sprintf("Validation error: %s", e.err)
}

// MethodDisabledError raised if a method is disabled in config file.
// For example, if user management is disabled, this error is raised
// every time an user operation is done using the REST API
type MethodDisabledError struct {
	err string
}

// Method disabled error details
func (e *MethodDisabledError) Error() string {
	return fmt.Sprintf("Method disabled error: %s", e.err)
}

// RecordNotFoundError raised if a requested user is not found
type RecordNotFoundError struct {
	err string
}

func (e *RecordNotFoundError) Error() string {
	return fmt.Sprintf("Not found: %s", e.err)
}

// GetProvider returns the configured provider
func GetProvider() Provider {
	return provider
}

// GetQuotaTracking returns the configured mode for user's quota tracking
func GetQuotaTracking() int {
	return config.TrackQuota
}

// Provider interface that data providers must implement.
type Provider interface {
	validateUserAndPass(username string, password string) (User, error)
	validateUserAndPubKey(username string, pubKey string) (User, string, error)
	updateQuota(username string, filesAdd int, sizeAdd int64, reset bool) error
	getUsedQuota(username string) (int, int64, error)
	userExists(username string) (User, error)
	addUser(user User) error
	updateUser(user User) error
	deleteUser(user User) error
	getUsers(limit int, offset int, order string, username string) ([]User, error)
	dumpUsers() ([]User, error)
	getUserByID(ID int64) (User, error)
	updateLastLogin(username string) error
	checkAvailability() error
	close() error
	reloadConfig() error
}

func init() {
	availabilityTicker = time.NewTicker(30 * time.Second)
}

// Initialize the data provider.
// An error is returned if the configured driver is invalid or if the data provider cannot be initialized
func Initialize(cnf Config, basePath string) error {
	var err error
	config = cnf
	sqlPlaceholders = getSQLPlaceholders()

	if len(config.ExternalAuthProgram) > 0 {
		if !filepath.IsAbs(config.ExternalAuthProgram) {
			return fmt.Errorf("invalid external auth program: %#v must be an absolute path", config.ExternalAuthProgram)
		}
		_, err := os.Stat(config.ExternalAuthProgram)
		if err != nil {
			providerLog(logger.LevelWarn, "invalid external auth program:: %v", err)
			return err
		}
	}
	if err := validateCredentialsDir(basePath); err != nil {
		return err
	}

	if config.Driver == SQLiteDataProviderName {
		err = initializeSQLiteProvider(basePath)
	} else if config.Driver == PGSQLDataProviderName {
		err = initializePGSQLProvider()
	} else if config.Driver == MySQLDataProviderName {
		err = initializeMySQLProvider()
	} else if config.Driver == BoltDataProviderName {
		err = initializeBoltProvider(basePath)
	} else if config.Driver == MemoryDataProviderName {
		err = initializeMemoryProvider(basePath)
	} else {
		err = fmt.Errorf("unsupported data provider: %v", config.Driver)
	}
	if err == nil {
		startAvailabilityTimer()
	}
	return err
}

// CheckUserAndPass retrieves the SFTP user with the given username and password if a match is found or an error
func CheckUserAndPass(p Provider, username string, password string) (User, error) {
	if len(config.ExternalAuthProgram) > 0 && (config.ExternalAuthScope == 0 || config.ExternalAuthScope&1 != 0) {
		user, err := doExternalAuth(username, password, "", "")
		if err != nil {
			return user, err
		}
		return checkUserAndPass(user, password)
	}
	return p.validateUserAndPass(username, password)
}

// CheckUserAndPubKey retrieves the SFTP user with the given username and public key if a match is found or an error
func CheckUserAndPubKey(p Provider, username string, pubKey string) (User, string, error) {
	if len(config.ExternalAuthProgram) > 0 && (config.ExternalAuthScope == 0 || config.ExternalAuthScope&2 != 0) {
		user, err := doExternalAuth(username, "", pubKey, "")
		if err != nil {
			return user, "", err
		}
		return checkUserAndPubKey(user, pubKey)
	}
	return p.validateUserAndPubKey(username, pubKey)
}

// CheckKeyboardInteractiveAuth checks the keyboard interactive authentication and returns
// the authenticated user or an error
func CheckKeyboardInteractiveAuth(p Provider, username, authProgram string, client ssh.KeyboardInteractiveChallenge) (User, error) {
	var user User
	var err error
	if len(config.ExternalAuthProgram) > 0 && (config.ExternalAuthScope == 0 || config.ExternalAuthScope&4 != 0) {
		user, err = doExternalAuth(username, "", "", "1")
	} else {
		user, err = p.userExists(username)
	}
	if err != nil {
		return user, err
	}
	return doKeyboardInteractiveAuth(user, authProgram, client)
}

// UpdateLastLogin updates the last login fields for the given SFTP user
func UpdateLastLogin(p Provider, user User) error {
	if config.ManageUsers == 0 {
		return &MethodDisabledError{err: manageUsersDisabledError}
	}
	return p.updateLastLogin(user.Username)
}

// UpdateUserQuota updates the quota for the given SFTP user adding filesAdd and sizeAdd.
// If reset is true filesAdd and sizeAdd indicates the total files and the total size instead of the difference.
func UpdateUserQuota(p Provider, user User, filesAdd int, sizeAdd int64, reset bool) error {
	if config.TrackQuota == 0 {
		return &MethodDisabledError{err: trackQuotaDisabledError}
	} else if config.TrackQuota == 2 && !reset && !user.HasQuotaRestrictions() {
		return nil
	}
	if config.ManageUsers == 0 {
		return &MethodDisabledError{err: manageUsersDisabledError}
	}
	return p.updateQuota(user.Username, filesAdd, sizeAdd, reset)
}

// GetUsedQuota returns the used quota for the given SFTP user.
// TrackQuota must be >=1 to enable this method
func GetUsedQuota(p Provider, username string) (int, int64, error) {
	if config.TrackQuota == 0 {
		return 0, 0, &MethodDisabledError{err: trackQuotaDisabledError}
	}
	return p.getUsedQuota(username)
}

// UserExists checks if the given SFTP username exists, returns an error if no match is found
func UserExists(p Provider, username string) (User, error) {
	return p.userExists(username)
}

// AddUser adds a new SFTP user.
// ManageUsers configuration must be set to 1 to enable this method
func AddUser(p Provider, user User) error {
	if config.ManageUsers == 0 {
		return &MethodDisabledError{err: manageUsersDisabledError}
	}
	err := p.addUser(user)
	if err == nil {
		go executeAction(operationAdd, user)
	}
	return err
}

// UpdateUser updates an existing SFTP user.
// ManageUsers configuration must be set to 1 to enable this method
func UpdateUser(p Provider, user User) error {
	if config.ManageUsers == 0 {
		return &MethodDisabledError{err: manageUsersDisabledError}
	}
	err := p.updateUser(user)
	if err == nil {
		go executeAction(operationUpdate, user)
	}
	return err
}

// DeleteUser deletes an existing SFTP user.
// ManageUsers configuration must be set to 1 to enable this method
func DeleteUser(p Provider, user User) error {
	if config.ManageUsers == 0 {
		return &MethodDisabledError{err: manageUsersDisabledError}
	}
	err := p.deleteUser(user)
	if err == nil {
		go executeAction(operationDelete, user)
	}
	return err
}

// DumpUsers returns an array with all users including their hashed password
func DumpUsers(p Provider) ([]User, error) {
	return p.dumpUsers()
}

// ReloadConfig reloads provider configuration.
// Currently only implemented for memory provider, allows to reload the users
// from the configured file, if defined
func ReloadConfig() error {
	return provider.reloadConfig()
}

// GetUsers returns an array of users respecting limit and offset and filtered by username exact match if not empty
func GetUsers(p Provider, limit int, offset int, order string, username string) ([]User, error) {
	return p.getUsers(limit, offset, order, username)
}

// GetUserByID returns the user with the given database ID if a match is found or an error
func GetUserByID(p Provider, ID int64) (User, error) {
	return p.getUserByID(ID)
}

// GetProviderStatus returns an error if the provider is not available
func GetProviderStatus(p Provider) error {
	return p.checkAvailability()
}

// Close releases all provider resources.
// This method is used in test cases.
// Closing an uninitialized provider is not supported
func Close(p Provider) error {
	availabilityTicker.Stop()
	availabilityTickerDone <- true
	return p.close()
}

func buildUserHomeDir(user *User) {
	if len(user.HomeDir) == 0 {
		if len(config.UsersBaseDir) > 0 {
			user.HomeDir = filepath.Join(config.UsersBaseDir, user.Username)
		}
	}
}

func validatePermissions(user *User) error {
	if len(user.Permissions) == 0 {
		return &ValidationError{err: "please grant some permissions to this user"}
	}
	permissions := make(map[string][]string)
	if _, ok := user.Permissions["/"]; !ok {
		return &ValidationError{err: fmt.Sprintf("permissions for the root dir \"/\" must be set")}
	}
	for dir, perms := range user.Permissions {
		if len(perms) == 0 {
			return &ValidationError{err: fmt.Sprintf("no permissions granted for the directory: %#v", dir)}
		}
		for _, p := range perms {
			if !utils.IsStringInSlice(p, ValidPerms) {
				return &ValidationError{err: fmt.Sprintf("invalid permission: %#v", p)}
			}
		}
		cleanedDir := filepath.ToSlash(path.Clean(dir))
		if cleanedDir != "/" {
			cleanedDir = strings.TrimSuffix(cleanedDir, "/")
		}
		if !path.IsAbs(cleanedDir) {
			return &ValidationError{err: fmt.Sprintf("cannot set permissions for non absolute path: %#v", dir)}
		}
		if utils.IsStringInSlice(PermAny, perms) {
			permissions[cleanedDir] = []string{PermAny}
		} else {
			permissions[cleanedDir] = perms
		}
	}
	user.Permissions = permissions
	return nil
}

func validatePublicKeys(user *User) error {
	if len(user.PublicKeys) == 0 {
		user.PublicKeys = []string{}
	}
	for i, k := range user.PublicKeys {
		_, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k))
		if err != nil {
			return &ValidationError{err: fmt.Sprintf("could not parse key nr. %d: %s", i, err)}
		}
	}
	return nil
}

func validateFilters(user *User) error {
	if len(user.Filters.AllowedIP) == 0 {
		user.Filters.AllowedIP = []string{}
	}
	if len(user.Filters.DeniedIP) == 0 {
		user.Filters.DeniedIP = []string{}
	}
	for _, IPMask := range user.Filters.DeniedIP {
		_, _, err := net.ParseCIDR(IPMask)
		if err != nil {
			return &ValidationError{err: fmt.Sprintf("could not parse denied IP/Mask %#v : %v", IPMask, err)}
		}
	}
	for _, IPMask := range user.Filters.AllowedIP {
		_, _, err := net.ParseCIDR(IPMask)
		if err != nil {
			return &ValidationError{err: fmt.Sprintf("could not parse allowed IP/Mask %#v : %v", IPMask, err)}
		}
	}
	return nil
}

func saveGCSCredentials(user *User) error {
	if user.FsConfig.Provider != 2 {
		return nil
	}
	if len(user.FsConfig.GCSConfig.Credentials) == 0 {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(user.FsConfig.GCSConfig.Credentials)
	if err != nil {
		return &ValidationError{err: fmt.Sprintf("could not validate GCS credentials: %v", err)}
	}
	err = ioutil.WriteFile(user.getGCSCredentialsFilePath(), decoded, 0600)
	if err != nil {
		return &ValidationError{err: fmt.Sprintf("could not save GCS credentials: %v", err)}
	}
	user.FsConfig.GCSConfig.Credentials = ""
	return nil
}

func validateFilesystemConfig(user *User) error {
	if user.FsConfig.Provider == 1 {
		err := vfs.ValidateS3FsConfig(&user.FsConfig.S3Config)
		if err != nil {
			return &ValidationError{err: fmt.Sprintf("could not validate s3config: %v", err)}
		}
		vals := strings.Split(user.FsConfig.S3Config.AccessSecret, "$")
		if !strings.HasPrefix(user.FsConfig.S3Config.AccessSecret, "$aes$") || len(vals) != 4 {
			accessSecret, err := utils.EncryptData(user.FsConfig.S3Config.AccessSecret)
			if err != nil {
				return &ValidationError{err: fmt.Sprintf("could not encrypt s3 access secret: %v", err)}
			}
			user.FsConfig.S3Config.AccessSecret = accessSecret
		}
		return nil
	} else if user.FsConfig.Provider == 2 {
		err := vfs.ValidateGCSFsConfig(&user.FsConfig.GCSConfig, user.getGCSCredentialsFilePath())
		if err != nil {
			return &ValidationError{err: fmt.Sprintf("could not validate GCS config: %v", err)}
		}
		return nil
	}
	user.FsConfig.Provider = 0
	user.FsConfig.S3Config = vfs.S3FsConfig{}
	user.FsConfig.GCSConfig = vfs.GCSFsConfig{}
	return nil
}

func validateBaseParams(user *User) error {
	if len(user.Username) == 0 || len(user.HomeDir) == 0 {
		return &ValidationError{err: "mandatory parameters missing"}
	}
	if len(user.Password) == 0 && len(user.PublicKeys) == 0 {
		return &ValidationError{err: "please set a password or at least a public_key"}
	}
	if !filepath.IsAbs(user.HomeDir) {
		return &ValidationError{err: fmt.Sprintf("home_dir must be an absolute path, actual value: %v", user.HomeDir)}
	}
	return nil
}

func validateUser(user *User) error {
	buildUserHomeDir(user)
	if err := validateBaseParams(user); err != nil {
		return err
	}
	if err := validatePermissions(user); err != nil {
		return err
	}
	if err := validateFilesystemConfig(user); err != nil {
		return err
	}
	if user.Status < 0 || user.Status > 1 {
		return &ValidationError{err: fmt.Sprintf("invalid user status: %v", user.Status)}
	}
	if len(user.Password) > 0 && !utils.IsStringPrefixInSlice(user.Password, hashPwdPrefixes) {
		pwd, err := argon2id.CreateHash(user.Password, argon2id.DefaultParams)
		if err != nil {
			return err
		}
		user.Password = pwd
	}
	if err := validatePublicKeys(user); err != nil {
		return err
	}
	if err := validateFilters(user); err != nil {
		return err
	}
	if err := saveGCSCredentials(user); err != nil {
		return err
	}
	return nil
}

func checkLoginConditions(user User) error {
	if user.Status < 1 {
		return fmt.Errorf("user %#v is disabled", user.Username)
	}
	if user.ExpirationDate > 0 && user.ExpirationDate < utils.GetTimeAsMsSinceEpoch(time.Now()) {
		return fmt.Errorf("user %#v is expired, expiration timestamp: %v current timestamp: %v", user.Username,
			user.ExpirationDate, utils.GetTimeAsMsSinceEpoch(time.Now()))
	}
	return nil
}

func checkUserAndPass(user User, password string) (User, error) {
	err := checkLoginConditions(user)
	if err != nil {
		return user, err
	}
	if len(user.Password) == 0 {
		return user, errors.New("Credentials cannot be null or empty")
	}
	match := false
	if strings.HasPrefix(user.Password, argonPwdPrefix) {
		match, err = argon2id.ComparePasswordAndHash(password, user.Password)
		if err != nil {
			providerLog(logger.LevelWarn, "error comparing password with argon hash: %v", err)
			return user, err
		}
	} else if strings.HasPrefix(user.Password, bcryptPwdPrefix) {
		if err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
			providerLog(logger.LevelWarn, "error comparing password with bcrypt hash: %v", err)
			return user, err
		}
		match = true
	} else if utils.IsStringPrefixInSlice(user.Password, pbkdfPwdPrefixes) {
		match, err = comparePbkdf2PasswordAndHash(password, user.Password)
		if err != nil {
			return user, err
		}
	} else if utils.IsStringPrefixInSlice(user.Password, unixPwdPrefixes) {
		match, err = compareUnixPasswordAndHash(user, password)
		if err != nil {
			return user, err
		}
	}
	if !match {
		err = errors.New("Invalid credentials")
	}
	return user, err
}

func checkUserAndPubKey(user User, pubKey string) (User, string, error) {
	err := checkLoginConditions(user)
	if err != nil {
		return user, "", err
	}
	if len(user.PublicKeys) == 0 {
		return user, "", errors.New("Invalid credentials")
	}
	for i, k := range user.PublicKeys {
		storedPubKey, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(k))
		if err != nil {
			providerLog(logger.LevelWarn, "error parsing stored public key %d for user %v: %v", i, user.Username, err)
			return user, "", err
		}
		if string(storedPubKey.Marshal()) == pubKey {
			fp := ssh.FingerprintSHA256(storedPubKey)
			return user, fp + ":" + comment, nil
		}
	}
	return user, "", errors.New("Invalid credentials")
}

func compareUnixPasswordAndHash(user User, password string) (bool, error) {
	match := false
	var err error
	if strings.HasPrefix(user.Password, sha512cryptPwdPrefix) {
		crypter, ok := unixcrypt.SHA512.CrypterFound(user.Password)
		if !ok {
			err = errors.New("cannot found matching SHA512 crypter")
			providerLog(logger.LevelWarn, "error comparing password with SHA512 crypt hash: %v", err)
			return match, err
		}
		if !crypter.Verify([]byte(password)) {
			return match, errWrongPassword
		}
		match = true
	} else if strings.HasPrefix(user.Password, md5cryptPwdPrefix) || strings.HasPrefix(user.Password, md5cryptApr1PwdPrefix) {
		crypter, ok := unixcrypt.MD5.CrypterFound(user.Password)
		if !ok {
			err = errors.New("cannot found matching MD5 crypter")
			providerLog(logger.LevelWarn, "error comparing password with MD5 crypt hash: %v", err)
			return match, err
		}
		if !crypter.Verify([]byte(password)) {
			return match, errWrongPassword
		}
		match = true
	} else {
		err = errors.New("unix crypt: invalid or unsupported hash format")
	}
	return match, err
}

func comparePbkdf2PasswordAndHash(password, hashedPassword string) (bool, error) {
	vals := strings.Split(hashedPassword, "$")
	if len(vals) != 5 {
		return false, fmt.Errorf("pbkdf2: hash is not in the correct format")
	}
	var hashFunc func() hash.Hash
	var hashSize int
	if strings.HasPrefix(hashedPassword, pbkdf2SHA256Prefix) {
		hashSize = sha256.Size
		hashFunc = sha256.New
	} else if strings.HasPrefix(hashedPassword, pbkdf2SHA512Prefix) {
		hashSize = sha512.Size
		hashFunc = sha512.New
	} else if strings.HasPrefix(hashedPassword, pbkdf2SHA1Prefix) {
		hashSize = sha1.Size
		hashFunc = sha1.New
	} else {
		return false, fmt.Errorf("pbkdf2: invalid or unsupported hash format %v", vals[1])
	}
	iterations, err := strconv.Atoi(vals[2])
	if err != nil {
		return false, err
	}
	salt := vals[3]
	expected := vals[4]
	df := pbkdf2.Key([]byte(password), []byte(salt), iterations, hashSize, hashFunc)
	buf := make([]byte, base64.StdEncoding.EncodedLen(len(df)))
	base64.StdEncoding.Encode(buf, df)
	return subtle.ConstantTimeCompare(buf, []byte(expected)) == 1, nil
}

// HideUserSensitiveData hides user sensitive data
func HideUserSensitiveData(user *User) User {
	user.Password = ""
	if user.FsConfig.Provider == 1 {
		user.FsConfig.S3Config.AccessSecret = utils.RemoveDecryptionKey(user.FsConfig.S3Config.AccessSecret)
	} else if user.FsConfig.Provider == 2 {
		user.FsConfig.GCSConfig.Credentials = ""
	}
	return *user
}

func addCredentialsToUser(user *User) error {
	if user.FsConfig.Provider != 2 {
		return nil
	}
	cred, err := ioutil.ReadFile(user.getGCSCredentialsFilePath())
	if err != nil {
		return err
	}
	user.FsConfig.GCSConfig.Credentials = base64.StdEncoding.EncodeToString(cred)
	return nil
}

func getSSLMode() string {
	if config.Driver == PGSQLDataProviderName {
		if config.SSLMode == 0 {
			return "disable"
		} else if config.SSLMode == 1 {
			return "require"
		} else if config.SSLMode == 2 {
			return "verify-ca"
		} else if config.SSLMode == 3 {
			return "verify-full"
		}
	} else if config.Driver == MySQLDataProviderName {
		if config.SSLMode == 0 {
			return "false"
		} else if config.SSLMode == 1 {
			return "true"
		} else if config.SSLMode == 2 {
			return "skip-verify"
		} else if config.SSLMode == 3 {
			return "preferred"
		}
	}
	return ""
}

func startAvailabilityTimer() {
	availabilityTickerDone = make(chan bool)
	checkDataprovider()
	go func() {
		for {
			select {
			case <-availabilityTickerDone:
				return
			case <-availabilityTicker.C:
				checkDataprovider()
			}
		}
	}()
}

func validateCredentialsDir(basePath string) error {
	if filepath.IsAbs(config.CredentialsPath) {
		credentialsDirPath = config.CredentialsPath
	} else {
		credentialsDirPath = filepath.Join(basePath, config.CredentialsPath)
	}
	fi, err := os.Stat(credentialsDirPath)
	if err == nil {
		if !fi.IsDir() {
			return errors.New("Credential path is not a valid directory")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(credentialsDirPath, 0700)
}

func checkDataprovider() {
	err := provider.checkAvailability()
	if err != nil {
		providerLog(logger.LevelWarn, "check availability error: %v", err)
	}
	metrics.UpdateDataProviderAvailability(err)
}

func terminateInteractiveAuthProgram(cmd *exec.Cmd, isFinished bool) {
	if isFinished {
		return
	}
	providerLog(logger.LevelInfo, "kill interactive auth program after an unexpected error")
	cmd.Process.Kill()
}

func doKeyboardInteractiveAuth(user User, authProgram string, client ssh.KeyboardInteractiveChallenge) (User, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, authProgram)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("SFTPGO_AUTHD_USERNAME=%v", user.Username),
		fmt.Sprintf("SFTPGO_AUTHD_PASSWORD=%v", user.Password))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return user, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return user, err
	}
	err = cmd.Start()
	if err != nil {
		return user, err
	}
	var once sync.Once
	scanner := bufio.NewScanner(stdout)
	authResult := 0
	for scanner.Scan() {
		var response keyboardAuthProgramResponse
		err := json.Unmarshal(scanner.Bytes(), &response)
		if err != nil {
			providerLog(logger.LevelInfo, "interactive auth error parsing response: %v", err)
			once.Do(func() { terminateInteractiveAuthProgram(cmd, false) })
			break
		}
		if response.AuthResult != 0 {
			authResult = response.AuthResult
			break
		}
		if len(response.Questions) == 0 {
			providerLog(logger.LevelInfo, "interactive auth error: program response does not contain questions")
			once.Do(func() { terminateInteractiveAuthProgram(cmd, false) })
			break
		}
		if len(response.Questions) != len(response.Echos) {
			providerLog(logger.LevelInfo, "interactive auth error, program response questions don't match echos: %v %v",
				len(response.Questions), len(response.Echos))
			once.Do(func() { terminateInteractiveAuthProgram(cmd, false) })
			break
		}
		go func() {
			questions := response.Questions
			answers, err := client(user.Username, response.Instruction, questions, response.Echos)
			if err != nil {
				providerLog(logger.LevelInfo, "error getting interactive auth client response: %v", err)
				once.Do(func() { terminateInteractiveAuthProgram(cmd, false) })
				return
			}
			if len(answers) != len(questions) {
				providerLog(logger.LevelInfo, "client answers does not match questions, expected: %v actual: %v", questions, answers)
				once.Do(func() { terminateInteractiveAuthProgram(cmd, false) })
				return
			}
			for _, answer := range answers {
				if runtime.GOOS == "windows" {
					answer += "\r"
				}
				answer += "\n"
				_, err = stdin.Write([]byte(answer))
				if err != nil {
					providerLog(logger.LevelError, "unable to write client answer to keyboard interactive program: %v", err)
					once.Do(func() { terminateInteractiveAuthProgram(cmd, false) })
					return
				}
			}
		}()
	}
	stdin.Close()
	once.Do(func() { terminateInteractiveAuthProgram(cmd, true) })
	go cmd.Process.Wait()
	if authResult != 1 {
		return user, fmt.Errorf("keyboard interactive auth failed, result: %v", authResult)
	}
	return user, nil
}

func doExternalAuth(username, password, pubKey, keyboardInteractive string) (User, error) {
	var user User
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pkey := ""
	if len(pubKey) > 0 {
		k, err := ssh.ParsePublicKey([]byte(pubKey))
		if err != nil {
			return user, err
		}
		pkey = string(ssh.MarshalAuthorizedKey(k))
	}
	cmd := exec.CommandContext(ctx, config.ExternalAuthProgram)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("SFTPGO_AUTHD_USERNAME=%v", username),
		fmt.Sprintf("SFTPGO_AUTHD_PASSWORD=%v", password),
		fmt.Sprintf("SFTPGO_AUTHD_PUBLIC_KEY=%v", pkey),
		fmt.Sprintf("SFTPGO_AUTHD_KEYBOARD_INTERACTIVE=%v", keyboardInteractive))
	out, err := cmd.Output()
	if err != nil {
		return user, fmt.Errorf("External auth error: %v", err)
	}
	err = json.Unmarshal(out, &user)
	if err != nil {
		return user, fmt.Errorf("Invalid external auth response: %v", err)
	}
	if len(user.Username) == 0 {
		return user, errors.New("Invalid credentials")
	}
	if len(password) > 0 {
		user.Password = password
	}
	if len(pkey) > 0 && !utils.IsStringPrefixInSlice(pkey, user.PublicKeys) {
		user.PublicKeys = append(user.PublicKeys, pkey)
	}
	u, err := provider.userExists(username)
	if err == nil {
		user.ID = u.ID
		user.UsedQuotaSize = u.UsedQuotaSize
		user.UsedQuotaFiles = u.UsedQuotaFiles
		user.LastQuotaUpdate = u.LastQuotaUpdate
		user.LastLogin = u.LastLogin
		err = provider.updateUser(user)
	} else {
		err = provider.addUser(user)
	}
	if err != nil {
		return user, err
	}
	return provider.userExists(username)
}

func providerLog(level logger.LogLevel, format string, v ...interface{}) {
	logger.Log(level, logSender, "", format, v...)
}

func executeNotificationCommand(operation string, user User) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	commandArgs := user.getNotificationFieldsAsSlice(operation)
	cmd := exec.CommandContext(ctx, config.Actions.Command, commandArgs...)
	cmd.Env = append(os.Environ(), user.getNotificationFieldsAsEnvVars(operation)...)
	startTime := time.Now()
	err := cmd.Run()
	providerLog(logger.LevelDebug, "executed command %#v with arguments: %+v, elapsed: %v, error: %v",
		config.Actions.Command, commandArgs, time.Since(startTime), err)
	return err
}

// executed in a goroutine
func executeAction(operation string, user User) {
	if !utils.IsStringInSlice(operation, config.Actions.ExecuteOn) {
		return
	}
	if operation != operationDelete {
		var err error
		user, err = provider.userExists(user.Username)
		if err != nil {
			providerLog(logger.LevelWarn, "unable to get the user to notify for operation %#v: %v", operation, err)
			return
		}
	}
	if len(config.Actions.Command) > 0 && filepath.IsAbs(config.Actions.Command) {
		// we are in a goroutine but if we have to send an HTTP notification we don't want to wait for the
		// end of the command
		if len(config.Actions.HTTPNotificationURL) > 0 {
			go executeNotificationCommand(operation, user)
		} else {
			executeNotificationCommand(operation, user)
		}
	}
	if len(config.Actions.HTTPNotificationURL) > 0 {
		var url *url.URL
		url, err := url.Parse(config.Actions.HTTPNotificationURL)
		if err != nil {
			providerLog(logger.LevelWarn, "Invalid http_notification_url %#v for operation %#v: %v", config.Actions.HTTPNotificationURL,
				operation, err)
			return
		}
		q := url.Query()
		q.Add("action", operation)
		url.RawQuery = q.Encode()
		HideUserSensitiveData(&user)
		userAsJSON, err := json.Marshal(user)
		if err != nil {
			return
		}
		startTime := time.Now()
		httpClient := &http.Client{
			Timeout: 15 * time.Second,
		}
		resp, err := httpClient.Post(url.String(), "application/json", bytes.NewBuffer(userAsJSON))
		respCode := 0
		if err == nil {
			respCode = resp.StatusCode
			resp.Body.Close()
		}
		providerLog(logger.LevelDebug, "notified operation %#v to URL: %v status code: %v, elapsed: %v err: %v",
			operation, url.String(), respCode, time.Since(startTime), err)
	}
}
