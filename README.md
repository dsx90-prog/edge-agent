# Edge Agent (Socket Proxy Service)

Микросервис для проксирования сокет команд в HTTP API вызовы и обеспечения интерактивного удаленного управления.

## Назначение

Сервис решает проблему управления парком устройств (терминалы, постаматы, IoT), которые находятся за NAT или имеют «серые» IP-адреса. Агент на устройстве сам инициирует соединение с центральным сервером, что позволяет управлять им напрямую, проксируя API-запросы, shell-команды и даже SSH-сессии через установленный WebSocket/TCP канал.

## Как это работает

```
Device (Grey IP) → Edge Agent (Dial out) → Central Server (Public IP)
                                                ↓
                                         Remote Management
```

## Архитектура

```
Socket/WS Client (Dashboard) → Edge Agent → HTTP API → Target Application
                                   ↳ PTY / SSH Session
```

## Основные возможности

- **Проксирование API**: Преобразование сокет-сообщений в HTTP-запросы.
- **Интерактивный терминал (PTY)**: Полноценный доступ к командной строке через WebSocket.
- **SSH Proxy**: Проксирование SSH трафика для удаленного управления.
- **Управление через WebSocket**: Поддержка постоянных соединений с сервером управления.
- **Локальные команды**: Выполнение shell-скриптов напрямую на устройстве.

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

### 3. `local_command` - выполнение локальной команды на устройстве
Позволяет выполнять shell команды локально на устройстве (host), где запущен агент:

```json
{
  "type": "local_command",
  "payload": {
    "command": "ls -la /home",
    "timeout": "30s"
  },
  "id": "123"
}
```

### 4. `quick_command` - выполнение предустановленных команд
Позволяет выполнять заранее определенные в конфиге команды.

## Тестирование и отладка

Для разработки и тестирования агента предусмотрен специальный тестовый сервер с веб-панелью управления.

Подробности в [README тестового сервера](test-server/README.md).

## Конфигурация

Скопируйте `config.example.yml` в `config.yml` и настройте параметры:

```yaml
server:
  host: "localhost"
  port: "8081"

api_proxy:
  base_url: "http://localhost:8080"
  timeout: "30s"
  headers:
    "User-Agent": "EdgeAgent/1.0"
  auth:
    token: "your-api-token-here"
    type: "Bearer"

websocket:
  enabled: true
  url: "ws://localhost:8082"
  client_id: "edge-agent-001"

logging:
  level: "info"
  file: "edge-agent.log"
```

## Запуск

```bash
# Сборка
make build

# Запуск
./edge-agent -config config.yml
```

## Развертывание

### systemd сервис
```bash
make create-service
make start-service
```

### Сборка для разных платформ
```bash
make build-linux    # Для Linux (amd64)
make build-rpi      # Для Raspberry Pi (arm64)
```
