# Leilões com fechamento automático — Go Expert

Sistema de leilões (auction) e lances (bid) em Go + MongoDB, baseado em
[devfullcycle/labs-auction-goexpert](https://github.com/devfullcycle/labs-auction-goexpert),
com **fechamento automático dos leilões via goroutines**.

## Como funciona o fechamento automático

Implementação em [`internal/infra/database/auction/create_auction.go`](internal/infra/database/auction/create_auction.go):

1. `GetAuctionDuration()` lê a duração do leilão da variável de ambiente `AUCTION_DURATION`.
2. Ao criar um leilão, `CreateAuction` insere o documento no MongoDB e dispara
   `go ar.closeAuctionWhenExpired(...)`, que não bloqueia a requisição.
3. A goroutine espera (`time.Timer`) até `timestamp de criação + AUCTION_DURATION` e então executa um
   `UpdateOne` alterando o `status` para `Completed` (fechado). O filtro `{_id, status: Active}` torna a
   operação idempotente.
4. A goroutine usa um contexto próprio (`context.Background()` com timeout), pois o contexto da requisição HTTP
   é cancelado assim que a resposta é enviada.
5. Na inicialização da aplicação, `ScheduleActiveAuctionsClosing` reagenda o fechamento dos leilões ainda ativos
   no banco (ex.: após um restart). Os que já expiraram são fechados imediatamente.

A validação de lances (`internal/infra/database/bid/create_bid.go`) usa a mesma `GetAuctionDuration()`, então
lances e fechamento seguem a mesma janela de tempo.

## Variáveis de ambiente

Arquivo: [`cmd/auction/.env`](cmd/auction/.env)

| Variável | Descrição | Exemplo |
|---|---|---|
| `AUCTION_DURATION` | Tempo que o leilão fica aberto após a criação. | `20s` |
| `AUCTION_INTERVAL` | Nome antigo, usado apenas se `AUCTION_DURATION` não estiver definida. | `20s` |
| `BATCH_INSERT_INTERVAL` | Intervalo máximo para gravar o lote de lances. | `20s` |
| `MAX_BATCH_SIZE` | Quantidade de lances por lote. | `4` |
| `MONGODB_URL` | String de conexão do MongoDB. | `mongodb://admin:admin@mongodb:27017/auctions?authSource=admin` |
| `MONGODB_DB` | Nome do banco. | `auctions` |
| `MONGO_INITDB_ROOT_USERNAME` / `MONGO_INITDB_ROOT_PASSWORD` | Usuário root criado no container do MongoDB. | `admin` |

`AUCTION_DURATION` usa o formato de `time.ParseDuration` do Go: `30s`, `5m`, `1h30m` etc.
Se o valor estiver ausente, inválido ou não for positivo, o padrão é **5 minutos**.

Para mudar o tempo, edite `AUCTION_DURATION` no `.env` e recrie o container:

```bash
docker compose up -d --build app
```

## Rodando o projeto

Pré-requisitos: Docker e Docker Compose.

```bash
docker compose up -d --build
```

A API sobe em `http://localhost:8080` e o MongoDB em `localhost:27017`.

### Testando manualmente

```bash
# 1. Criar um leilão (condition: 0, 1 ou 2)
curl -X POST http://localhost:8080/auction \
  -H "Content-Type: application/json" \
  -d '{"product_name":"Notebook","category":"Electronics","description":"Notebook em otimo estado","condition":1}'

# 2. Listar leilões para pegar o id (status 0 = Active)
curl http://localhost:8080/auction

# 3. Após AUCTION_DURATION, consultar novamente: status 1 = Completed
curl http://localhost:8080/auction/<auctionId>
```

No **PowerShell** (Windows), use `Invoke-RestMethod`, porque lá `curl` é um apelido de `Invoke-WebRequest`:

```powershell
$body = '{"product_name":"Notebook","category":"Electronics","description":"Notebook em otimo estado","condition":1}'
Invoke-RestMethod -Method Post -Uri http://localhost:8080/auction -ContentType "application/json" -Body $body
$id = (Invoke-RestMethod http://localhost:8080/auction | Sort-Object timestamp)[-1].id
Invoke-RestMethod http://localhost:8080/auction/$id   # status 0
Start-Sleep -Seconds 22
Invoke-RestMethod http://localhost:8080/auction/$id   # status 1
```

Os logs mostram o fechamento: `docker compose logs -f app` → `Auction id = ... closed automatically`.

### Endpoints

| Método | Rota | Descrição |
|---|---|---|
| POST | `/auction` | Cria leilão |
| GET | `/auction` | Lista leilões (`?status=`, `?category=`, `?productName=`) |
| GET | `/auction/:auctionId` | Busca leilão por id |
| GET | `/auction/winner/:auctionId` | Lance vencedor |
| POST | `/bid` | Cria lance |
| GET | `/bid/:auctionId` | Lances do leilão |
| GET | `/user/:userId` | Busca usuário |

## Testes

Os testes de fechamento ficam em
[`internal/infra/database/auction/create_auction_test.go`](internal/infra/database/auction/create_auction_test.go)
e usam um MongoDB real (cada teste cria e remove um banco temporário):

- `TestCreateAuction_ClosesAutomaticallyAfterAuctionDuration`: cria um leilão com `AUCTION_DURATION=2s`, confirma
  que ele está `Active`, que continua `Active` antes do prazo e que passa para `Completed` sem intervenção manual.
- `TestScheduleActiveAuctionsClosing_ClosesExpiredAuctions`: um leilão ativo e já expirado é fechado ao reagendar.
- `TestGetAuctionDuration`: leitura da variável de ambiente e valores padrão.

**Via Docker (recomendado):**

```bash
docker compose --profile test run --rm test
```

**Localmente** (com o MongoDB do compose rodando):

```bash
docker compose up -d mongodb
go test -v ./...
```

Por padrão o teste conecta em `mongodb://admin:admin@localhost:27017/?authSource=admin`; use
`MONGODB_TEST_URL` para outra URL. Se o MongoDB não estiver acessível, os testes de integração são ignorados (skip).

## Ajustes feitos no projeto base

- `docker-compose.yml`: removido o `command: /auction` (o binário fica em `/app/auction`), adicionados
  healthcheck do MongoDB, `depends_on` e o serviço `test`.
- `.env`: `MONGO_INITDB_ROOT_*` usavam `:` em vez de `=`, formato que o `env_file` do Docker não reconhece.
- `configuration/logger`: o zap não tinha `OutputPaths`, então nenhum log era exibido.
- `GET /auction` sem `?status=` retornava 400; agora a ausência do parâmetro equivale a `status=0` (sem filtro).
- `internal/infra/database/user/find_user.go`: `%d` com string quebrava o `go vet` (e portanto o `go test ./...`).
