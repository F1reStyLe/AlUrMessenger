-- Таблица пользователей
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    name VARCHAR(50) UNIQUE NOT NULL,
    email VARCHAR(255) UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ,
    is_online BOOLEAN DEFAULT FALSE
);

-- Индексы для быстрого поиска по email и name
CREATE INDEX idx_user_email ON users (email);
CREATE INDEX idx_user_name ON users (name);

-- Таблица чатов
CREATE TABLE IF NOT EXISTS chats (
    id SERIAL PRIMARY KEY,
    type VARCHAR(10) NOT NULL CHECK (type IN ('direct', 'group')),
    name VARCHAR(100),
    created_at TIMESTAMPTZ DEFAULT NOW()
);

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