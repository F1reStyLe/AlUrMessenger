// Package devinit создаёт только локальные development secrets, без ротации
// существующих credentials или изменения данных Docker volumes.
package devinit

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

// Initialize разрешён исключительно в development. Каталог закрыт от других
// пользователей; файлы readable для non-root контейнеров через Docker secret mounts.
// Повторный запуск сверяет derived URLs и не перезаписывает существующие данные.
func Initialize(environment, directory string) error {
	if environment != "development" {
		return errors.New("DEV_INIT_REQUIRES_DEVELOPMENT")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return errors.New("DEV_DIRECTORY_UNAVAILABLE")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("DEV_DIRECTORY_INVALID")
	}
	if err = os.Chmod(directory, 0700); err != nil {
		return errors.New("DEV_DIRECTORY_PERMISSIONS_FAILED")
	}
	secrets := map[string]string{}
	for _, name := range []string{"pg-admin-password", "pg-runtime-password", "pg-migration-password", "redis-password", "minio-admin-password", "minio-runtime-password"} {
		path := filepath.Join(directory, name)
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("DEV_SECRET_NOT_REGULAR")
			}
			data, err := os.ReadFile(path)
			if err != nil || len(data) != 64 {
				return errors.New("DEV_SECRET_INVALID")
			}
			if _, err := hex.DecodeString(string(data)); err != nil {
				return errors.New("DEV_SECRET_INVALID")
			}
			secrets[name] = string(data)
		} else if os.IsNotExist(err) {
			data := make([]byte, 32)
			if _, err := rand.Read(data); err != nil {
				return errors.New("DEV_RANDOM_FAILED")
			}
			secrets[name] = hex.EncodeToString(data)
			if err := create(path, []byte(secrets[name])); err != nil {
				return err
			}
		} else {
			return errors.New("DEV_SECRET_UNAVAILABLE")
		}
	}
	derived := map[string]string{
		"postgres-url":           "postgres://alur_runtime:" + secrets["pg-runtime-password"] + "@postgres:5432/alur?sslmode=disable",
		"migration-postgres-url": "postgres://alur_migrator:" + secrets["pg-migration-password"] + "@postgres:5432/alur?sslmode=disable",
		"redis-url":              "redis://:" + secrets["redis-password"] + "@redis:6379/0",
		"redis.conf":             "bind 0.0.0.0\nappendonly yes\nrequirepass " + secrets["redis-password"] + "\n",
		"minio-access-key":       "alur-runtime",
	}
	for name, value := range derived {
		path := filepath.Join(directory, name)
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("DEV_SECRET_NOT_REGULAR")
			}
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, []byte(value)) {
				return errors.New("DEV_DERIVED_SECRET_MISMATCH")
			}
		} else if os.IsNotExist(err) {
			if err := create(path, []byte(value)); err != nil {
				return err
			}
		} else {
			return errors.New("DEV_SECRET_UNAVAILABLE")
		}
	}
	return nil
}

// create использует O_EXCL, поэтому конкурентный initializer никогда не заменит
// уже записанный secret. При таком конфликте повторите команду после завершения первого.
func create(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0444)
	if err != nil {
		return errors.New("DEV_SECRET_CREATE_FAILED")
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("DEV_SECRET_WRITE_FAILED")
	}
	return nil
}
