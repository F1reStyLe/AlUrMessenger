--+migrate Up
-- Таблица активности пользователей
CREATE TABLE users (
    id int PRIMARY KEY,
    user_id int UNIQUE NOT NULL,
    username VARCHAR(30) UNIQUE NOT NULL,
    last_seen_at TIMESTAMPTZ,
    is_online BOOLEAN DEFAULT FALSE
)

CREATE INDEX idx_users_user_id ON users(user_id);

CREATE TABLE IF NOT EXISTS chat_types (
    id SERIAL PRIMARY KEY,
    name VARCHAR(20) UNIQUE NOT NULL
)

-- Таблица чатов
CREATE TABLE IF NOT EXISTS chats (
    id SERIAL PRIMARY KEY,
    type int NOT NULL REFERENCES chat_types(id) ON DELETE CASCADE,
    name VARCHAR(100),
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- Индексы для чатов
CREATE INDEX idx_chat_type ON chats(type);

-- Участники чатов
CREATE TABLE IF NOT EXISTS chat_members (
    chat_id INT REFERENCES chats(id) ON DELETE CASCADE,
    user_id INT REFERENCES users(id) ON DELETE CASCADE,
    joined_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (chat_id, user_id)
);

CREATE INDEX idx_chat_member_chat_id ON chat_members(chat_id);
CREATE INDEX idx_chat_member_user_id ON chat_members(user_id);

-- Таблица сообщений
CREATE TABLE IF NOT EXISTS messages (
    id SERIAL PRIMARY KEY,
    chat_id INT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    is_edited BOOLEAN DEFAULT FALSE
);

-- Индексы для сообщений
CREATE INDEX idx_message_chat_id ON messages(chat_id);
CREATE INDEX idx_message_user_id ON messages(user_id);
CREATE INDEX idx_message_created_at ON messages(created_at);

-- +migrate Down
DROP TABLE IF EXISTS user_activities;
DROP TABLE IF EXISTS chat_types;
DROP TABLE IF EXISTS chats;
DROP TABLE IF EXISTS chat_members;
DROP TABLE IF EXISTS messages;