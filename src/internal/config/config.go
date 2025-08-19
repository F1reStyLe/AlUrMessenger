package config

import (
	"os"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
	"github.com/spf13/viper"
)

type Config struct {
	Environment string          `yaml:"environment" env-default:"local"`
	HTTP        HTTPConfig      `yaml:"http"`
	Database    DatabaseConfig  `yaml:"database"`
	JWT         JWTConfig       `yaml:"jwt"`
	WebSocket   WebSocketConfig `yaml:"websocket"`
}

type HTTPConfig struct {
	Host string `yaml:"host" env-default:"localhost"`
	Port string `yaml:"port" env-default:"8081"`
	// Таймаут для graceful shutdown
	ReadTimeout time.Duration `yaml:"read_timeout" env-default:"10s"`
}

type DatabaseConfig struct {
	Host     string `yaml:"host" env-default:"localhost"`
	Port     string `yaml:"port" env-default:"6532"`
	Name     string `yaml:"name" env-default:"messenger"`
	User     string `yaml:"user" env-default:"postgres"`
	Password string `yaml:"password" env-required:"true"` // Обязательный параметр
	SSLMode  string `yaml:"ssl_mode" env-default:"disable"`
	// Максимальное количество открытых соединений с БД
	MaxOpenConns int `yaml:"max_open_conns" env-default:"25"`
	MaxIdleConns int `yaml:"max_idle_conns" env-default:"25"`
}

type JWTConfig struct {
	SecretKey       string        `yaml:"secret_key" env-required:"true"` // Обязательный параметр
	AccessTokenTTL  time.Duration `yaml:"access_token_ttl" env-default:"3h"`
	RefreshTokenTTL time.Duration `yaml:"refresh_token_ttl" env-default:"720h"` // 30 дней
}

type WebSocketConfig struct {
	ReadBufferSize  int `yaml:"read_buffer_size" env-default:"1024"`
	WriteBufferSize int `yaml:"write_buffer_size" env-default:"1024"`
	// Таймаут для пинга/понга
	PingPeriod time.Duration `yaml:"ping_period" env-default:"60s"`
}

// MustLoad загружает конфигурацию и паникует в случае ошибки
// Обычно вызывается в main.go
func MustLoad() *Config {
	configPath := fetchConfigPath()
	if configPath == "" {
		panic("config path is empty")
	}

	return MustLoadPath(configPath)
}

// MustLoadPath загружает конфигурацию по конкретному пути
func MustLoadPath(configPath string) *Config {
	// Проверяем существование файла конфигурации
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		panic("config file does not exist: " + configPath)
	}

	var cfg Config

	// Читаем конфиг файл с помощью Viper
	viper.SetConfigFile(configPath)
	if err := viper.ReadInConfig(); err != nil {
		panic("failed to read config file: " + err.Error())
	}

	// Преобразуем прочитанные данные в структуру
	if err := viper.Unmarshal(&cfg); err != nil {
		panic("failed to unmarshal config: " + err.Error())
	}

	// Валидируем конфигурацию с помощью cleanenv
	if err := cleanenv.ReadEnv(&cfg); err != nil {
		panic("failed to read env: " + err.Error())
	}

	return &cfg
}

// fetchConfigPath получает путь до конфиг файла из флагов или env переменных
// Приоритет: флаг > env переменная > дефолтное значение
func fetchConfigPath() string {
	var res string

	// Флаг имеет наивысший приоритет
	// --config="path/to/config.yaml"
	viper.SetDefault("config", "") // Дефолтное значение для флага
	if viper.GetString("config") != "" {
		res = viper.GetString("config")
	}

	// Если флаг не установлен, проверяем env переменную
	if res == "" {
		res = os.Getenv("CONFIG_PATH")
	}

	// Если env переменная не установлена, используем дефолтный путь
	if res == "" {
		res = "config/local.yaml" // Дефолтный путь относительно места запуска
	}

	return res
}
