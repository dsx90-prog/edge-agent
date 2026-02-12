# Socket Proxy Service

Микросервис для проксирования сокет команд в HTTP API вызовы.

## Назначение

Сервис принимает команды через TCP сокет и перенаправляет их на HTTP эндпоинты целевого API. Это позволяет отвязать логику сокет клиента от конкретной реализации приложения и управлять любым сервисом через единый сокет интерфейс.

## Архитектура

```
Socket Client → Socket Proxy Service → HTTP API → Target Application
```

## Поддерживаемые команды

### 1. `api_call` - вызов эндпоинта с относительным путем
Использует `base_url` из конфигурации:

```json
{
  "type": "api_call",
  "payload": {
    "url": "/api/v1/cell/open",
    "method": "POST",
    "headers": {"Content-Type": "application/json"},
    "body": {"cell_number": 1, "reason": "test"}
  },
  "id": "123"
}
```

### 2. `http_request` - вызов с полным URL
Игнорирует `base_url`, использует полный URL:

```json
{
  "type": "http_request",
  "payload": {
    "url": "http://external-service.com/api/something",
    "method": "POST",
    "headers": {"Authorization": "Bearer token"},
    "body": {"data": "value"}
  },
  "id": "123"
}
```

## Поддерживаемые команды

### 1. `api_call` - вызов эндпоинта с относительным путем
Использует `base_url` из конфигурации:

```json
{
  "type": "api_call",
  "payload": {
    "url": "/api/v1/cell/open",
    "method": "POST",
    "headers": {"Content-Type": "application/json"},
    "body": {"cell_number": 1, "reason": "test"}
  },
  "id": "123"
}
```

### 2. `http_request` - вызов с полным URL
Игнорирует `base_url`, использует полный URL:

```json
{
  "type": "http_request",
  "payload": {
    "url": "http://external-service.com/api/something",
    "method": "POST",
    "headers": {"Authorization": "Bearer token"},
    "body": {"data": "value"}
  },
  "id": "123"
}
```

### 3. `ssh_command` - выполнение команды на удаленном сервере
Позволяет выполнять SSH команды на удаленных серверах:

```json
{
  "type": "ssh_command",
  "payload": {
    "ssh_config": {
      "host": "192.168.1.100",
      "port": 22,
      "username": "admin",
      "password": "password",
      "private_key": "-----BEGIN RSA PRIVATE KEY-----\n...",
      "timeout": "30s"
    },
    "command": "ls -la /home",
    "env": {"VAR1": "value1", "VAR2": "value2"},
    "work_dir": "/tmp"
  },
  "id": "123"
}
```

### 4. `quick_command` - выполнение предустановленных команд
Позволяет выполнять заранее определенные команды:

```json
{
  "type": "quick_command",
  "payload": {
    "command": "restart_server"
  },
  "id": "123"
}
```

### 5. WebSocket клиент (новая функция)
При включении в конфигурации, сервис может подключаться к внешним WebSocket серверам:

```yaml
websocket:
  enabled: true
  url: "ws://localhost:8082/commands"
  client_id: "socket-proxy-client"
  auth:
    enabled: true
    secret: "your-jwt-secret"
    issuer: "socket-proxy"
  reconnect:
    enabled: true
    max_attempts: 5
    initial_delay: "5s"
    max_delay: "60s"
    backoff_multiplier: 2
```

#### Встроенные быстрые команды:
- `restart_server` - перезапуск nginx на удаленном сервере
- `reboot_server` - перезагрузка удаленного сервера  
- `check_disk_space` - проверка дискового пространства
- `open_cell_1` - открытие ячейки 1 через API
- `get_status` - получение статуса приложения
- `ping_external` - проверка доступности внешнего сервиса

#### Параметры SSH конфигурации:
- `host` - адрес сервера (обязательно)
- `port` - порт SSH (по умолчанию 22)
- `username` - имя пользователя (обязательно)
- `password` - пароль (опционально)
- `private_key` - приватный ключ (опционально)
- `timeout` - таймаут подключения (по умолчанию 30s)

#### Параметры команды:
- `command` - выполняемая команда (обязательно)
- `env` - переменные окружения (опционально)
- `work_dir` - рабочая директория (опционально)

## Поток выполнения

```
1. Socket Client → Socket Proxy Service
   Отправка JSON команды

2. Socket Proxy Service → HTTP API  
   HTTP запрос к целевому API

3. HTTP API → Socket Proxy Service
   Ответ от API

4. Socket Proxy Service → Socket Client
   Проксирование ответа обратно клиенту
```

## Конфигурация

Скопируйте `config.example.yml` в `config.yml`:

```yaml
server:
  host: "localhost"
  port: "8081"

api_proxy:
  base_url: "http://localhost:8080"  # Base URL для api_call команд
  timeout: "30s"
  
  # Общие заголовки для всех запросов
  headers:
    "User-Agent": "SocketProxyService/1.0"
  
  # Аутентификация для всех запросов
  auth:
    token: "your-api-token-here"
    type: "Bearer"

# Quick commands - predefined commands for common operations
quick_commands:
  restart_server:
    type: "ssh_command"
    payload:
      ssh_config:
        host: "192.168.1.100"
        username: "admin"
        password: "password"
      command: "systemctl restart nginx"

# Enable/disable specific command types
enabled_commands:
  api_call: true      # Enable/disable API calls
  http_request: true  # Enable/disable HTTP requests
  ssh_command: true   # Enable/disable SSH commands

logging:
  level: "info"
  format: "text"
  file: "socket-proxy.log"
```

## Управление доступными командами

### Включение/отключение типов команд

В конфигурации можно включать или отключать определенные типы команд:

```yaml
enabled_commands:
  api_call: true      # Разрешить API вызовы
  http_request: false # Запретить HTTP запросы
  ssh_command: true   # Разрешить SSH команды
```

Если тип команды отключен, сервис вернет ошибку:
```json
{
  "id": "123",
  "success": false,
  "error": "http_request commands are disabled"
}
```

### Создание быстрых команд

Можно добавлять собственные быстрые команды в конфигурации:

```yaml
quick_commands:
  my_custom_command:
    type: "api_call"
    payload:
      url: "/api/custom/action"
      method: "POST"
      body:
        param1: "value1"
        param2: "value2"
  
  backup_database:
    type: "ssh_command"
    payload:
      ssh_config:
        host: "backup-server.local"
        username: "backup"
        private_key: "/path/to/key"
      command: "/scripts/backup.sh"
      work_dir: "/opt/backups"
```

```yaml
server:
  host: "localhost"
  port: "8081"

api_proxy:
  base_url: "http://localhost:8080"  # Base URL для api_call команд
  timeout: "30s"
  
  # Общие заголовки для всех запросов
  headers:
    "User-Agent": "SocketProxyService/1.0"
  
  # Аутентификация для всех запросов
  auth:
    token: "your-api-token-here"
    type: "Bearer"

logging:
  level: "info"
  format: "text"
  file: "socket-proxy.log"
```

## Запуск

```bash
# Сборка
make build

# Запуск
make run

# Или напрямую
./socket-proxy-service -config config.yml
```

## Пример использования

### 1. Запуск сервиса
```bash
./socket-proxy-service
```

### 2. Подключение к сокету
```bash
telnet localhost 8081
```

### 3. Отправка команды
```json
{"type":"api_call","payload":{"url":"/api/cell/open","method":"POST","body":{"cell_number":1}},"id":"123"}
```

### 4. Пример ответа для SSH команды
```json
{
  "id": "123",
  "success": true,
  "data": {
    "exit_code": 0,
    "stdout": "total 8\ndrwxr-xr-x  2 root root 4096 Feb  9 15:30 .\ndrwxr-xr-x  3 root root 4096 Feb  9 15:25 ..\n-rw-r--r--  1 root root  220 Feb  9 15:30 file1.txt\n",
    "stderr": "",
    "duration": "125.5ms"
  }
}
```

## Требования к HTTP API

Целевое API должно возвращать ответы в формате:

```json
{
  "success": true|false,
  "data": {...},
  "error": "error message"
}
```

### Рекомендуемые эндпоинты

#### POST /api/cell/open
```json
Request: {"cell_number": 1, "reason": "test", "skip_db": false}
Response: {"success": true, "data": {"message": "Cell opened"}}
```

#### POST /api/cell/status
```json
Request: {"cell_number": 1}
Response: {"success": true, "data": {"status": "open", "code": 123}}
```

#### POST /api/key/add
```json
Request: {"cell_number": 1, "pin_code": 1234, "description": "Test"}
Response: {"success": true, "data": {"message": "Key added"}}
```

#### POST /api/key/delete
```json
Request: {"cell_number": 1, "key_id": "key123"}
Response: {"success": true, "data": {"message": "Key deleted"}}
```

## Health Check

Сервис выполняет health check целевого API при запуске:
- GET `{base_url}/health`
- Если API недоступно, в логах будет предупреждение

## Логирование

Поддерживаемые уровни: `debug`, `info`, `warn`, `error`
- Вывод в консоль и/или файл
- Логирование всех запросов и ответов

## Аутентификация

Поддерживается Bearer и Basic аутентификация:
- Глобальная настройка в конфигурации
- Переопределение для конкретного запроса через заголовки

## Graceful Shutdown

Сервис поддерживает корректное завершение работы:
- Обработка сигналов SIGINT и SIGTERM
- Завершение активных соединений
- Сохранение логов

## Развертывание

### systemd сервис
```bash
make create-service
make start-service
```

### Сборка для разных платформ
```bash
make build-linux    # Для Linux
make build-rpi      # Для Raspberry Pi
```

## Преимущества архитектуры

- **Разделение ответственности** - каждое приложение делает свою работу
- **Независимое развертывание** - прокси сервис можно запускать отдельно
- **Масштабируемость** - можно управлять несколькими приложениями
- **Универсальность** - работает с любым HTTP API
- **Отладка** - централизованное логирование всех запросов
