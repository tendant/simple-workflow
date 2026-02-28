# simple-workflow

Manage workflows via the simple-workflow REST API.

## Server

Base URL: `http://localhost:8080/api/v1`

Start the server if not running:

```bash
cd /Users/lei/workspace/workflow/simple-workflow
go run ./cmd/server --db "sqlite://workflow.db" --migrate
```

Check health: `curl -s http://localhost:8080/api/v1/health`

If the server requires an API key, pass `-H "X-API-Key: <key>"` on every request.

## Workflows

### Submit

```bash
curl -s -X POST http://localhost:8080/api/v1/workflows \
  -H "Content-Type: application/json" \
  -d '{"type":"<workflow-type>","payload":<json>,"priority":0,"max_attempts":3,"idempotency_key":"<optional>"}'
```

Response (201): `{"id":"<run-id>"}`

### List

```bash
curl -s 'http://localhost:8080/api/v1/workflows?type=<type>&status=<status>&limit=50&offset=0'
```

All query params are optional. Returns a JSON array of workflow runs.

### Get details

```bash
curl -s http://localhost:8080/api/v1/workflows/<id>
```

### Get events

```bash
curl -s http://localhost:8080/api/v1/workflows/<id>/events
```

### Cancel

```bash
curl -s -X DELETE http://localhost:8080/api/v1/workflows/<id>
```

Response: `{"status":"cancelled"}`

## Schedules

### Create

```bash
curl -s -X POST http://localhost:8080/api/v1/schedules \
  -H "Content-Type: application/json" \
  -d '{"type":"<workflow-type>","cron":"*/5 * * * *","timezone":"UTC","payload":<json>,"priority":0,"max_attempts":3}'
```

Response (201): `{"id":"<schedule-id>"}`

### List

```bash
curl -s http://localhost:8080/api/v1/schedules
```

### Pause / Resume

```bash
curl -s -X PATCH http://localhost:8080/api/v1/schedules/<id>/pause
curl -s -X PATCH http://localhost:8080/api/v1/schedules/<id>/resume
```

### Delete

```bash
curl -s -X DELETE http://localhost:8080/api/v1/schedules/<id>
```
