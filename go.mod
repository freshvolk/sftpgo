module github.com/freshvolk/sftpgo

go 1.13

require (
	cloud.google.com/go/storage v1.5.0
	github.com/alexedwards/argon2id v0.0.0-20190612080829-01a59b2b8802
	github.com/aws/aws-sdk-go v1.28.9
	github.com/drakkan/sftpgo v0.0.0-20200205211703-553cceab4201
	github.com/eikenb/pipeat v0.0.0-20190316224601-fb1f3a9aa29f
	github.com/go-chi/chi v4.0.3+incompatible
	github.com/go-chi/render v1.0.1
	github.com/go-sql-driver/mysql v1.5.0
	github.com/grandcat/zeroconf v1.0.0
	github.com/lib/pq v1.3.0
	github.com/mattn/go-sqlite3 v2.0.3+incompatible
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.1 // indirect
	github.com/nathanaelle/password v1.0.0
	github.com/pkg/sftp v1.11.0
	github.com/prometheus/client_golang v1.4.0
	github.com/rs/xid v1.2.1
	github.com/rs/zerolog v1.17.2
	github.com/spf13/cobra v0.0.5
	github.com/spf13/viper v1.6.2
	go.etcd.io/bbolt v1.3.3
	golang.org/x/crypto v0.0.0-20200128174031-69ecbb4d6d5d
	golang.org/x/sys v0.0.0-20200124204421-9fbb57f87de9
	google.golang.org/api v0.15.0
	gopkg.in/natefinch/lumberjack.v2 v2.0.0
)

replace github.com/eikenb/pipeat v0.0.0-20190316224601-fb1f3a9aa29f => github.com/drakkan/pipeat v0.0.0-20200123131427-11c048cfc0ec
