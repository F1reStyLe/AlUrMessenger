package config

import (
	"fmt"
	"os"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
	"github.com/spf13/viper"
)

type Config struct {
	Environment string         `yaml:"environment" env-default:"local"`
	Http        HttpConfig     `yaml:"http"`
	Database    DatabaseConfig `yaml:"database"`
	JWT         JWTConfig      `yaml:"jwt"`
}

type HttpConfig struct {
	Host string `yaml:"host" env-default:"localhost"`
	Port string `yaml:"port" env-default:"8085"`
}

type DatabaseConfig struct {
	Host     string `yaml:"host" env-default:"postgres"`
	Port     string `yaml:"dbport" env-default:"5432"`
	Name     string `yaml:"name" env-default:"auth"`
	User     string `yaml:"user" env-default:"postgres"`
	Password string `yaml:"password"` // Обязательный параметр
	SSLMode  string `yaml:"ssl_mode" env-default:"disable"`
	// Максимальное количество открытых соединений с БД
	MaxOpenConns int `yaml:"max_open_conns" env-default:"25"`
	MaxIdleConns int `yaml:"max_idle_conns" env-default:"25"`
}

type JWTConfig struct {
	SecretKey       string        `yaml:"secret_key"` // Обязательный параметр
	AccessTokenTTL  time.Duration `yaml:"access_token_ttl" env-default:"3h"`
	RefreshTokenTTL time.Duration `yaml:"refresh_token_ttl" env-default:"720h"` // 30 дней
}

// MustLoad загружает конфигурацию и паникует в случае ошибки
func MustLoad() *Config {
	configPath := fetchConfigPath()
	if configPath == "" {
		panic("config path is empty")
	}

	return MustLoadPath(configPath)
}

// MustLoadPath загружает конфигурацию по конкретному пути
func MustLoadPath(configPath string) *Config {
	// Проверяем, что файл существует
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		panic(fmt.Sprintf("config file does not exist: %s", configPath))
	} else if err != nil {
		panic(fmt.Sprintf("error accessing config file: %v", err))
	}

	var cfg Config

	// Настройка Viper
	viper.SetConfigFile(configPath)

	// Опционально: можно указать тип (если путь без расширения)
	viper.SetConfigType("yaml")

	// Читаем конфиг
	if err := viper.ReadInConfig(); err != nil {
		panic(fmt.Sprintf("failed to read config file: %v", err))
	}

	// Десериализуем в структуру
	if err := viper.Unmarshal(&cfg); err != nil {
		panic(fmt.Sprintf("failed to unmarshal config: %v", err))
	}

	// Загружаем переменные окружения (и переопределяем ими значения из YAML)
	if err := cleanenv.ReadEnv(&cfg); err != nil {
		panic(fmt.Sprintf("failed to read env vars: %v", err))
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
		res = "./config/local.yaml" // Дефолтный путь относительно места запуска
	}

	return res
}

func (d *DatabaseConfig) GetDBConnectionString() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}
