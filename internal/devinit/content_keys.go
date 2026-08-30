package devinit

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/F1reStyLe/AlUrMessenger/internal/cryptography"
)

// initializeContentKeys never rotates an existing keyring: lost keys would make
// stored messages unreadable. An invalid file must be repaired from backup explicitly.
func initializeContentKeys(directory string) error {
	path := filepath.Join(directory, "content-keys.json")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return cryptography.ErrCrypto
		}
		_, err := cryptography.Load(path)
		return err
	} else if !os.IsNotExist(err) {
		return cryptography.ErrCrypto
	}
	keys, err := cryptography.Generate()
	if err != nil {
		return err
	}
	data, err := json.Marshal(keys)
	if err != nil {
		return cryptography.ErrCrypto
	}
	return create(path, data)
}
