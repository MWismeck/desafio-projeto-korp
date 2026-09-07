# Decisões técnicas — http-server-projeto-korp

| Campo | Valor |
|---|---|
| Projeto | Desafio DevOps "Projeto Korp" — serviço `http-server-projeto-korp` |
| Autor | MWismeck |
| Repositório | https://github.com/MWismeck/desafio-projeto-korp |
| Versão do documento | 1.0.0 — 2026-09-07 |
| Ambiente de desenvolvimento | Windows 11 + Git Bash; Go 1.24.3; Docker Desktop 28 |
| Ambiente alvo (Docker/Ansible) | Linux: WSL2 Ubuntu 24.04 ou VM Ubuntu 24.04 (o playbook trata os dois; ver §5.8) |
| Guia passo a passo | `README.md` (pré-requisitos, comando único, saídas esperadas, troubleshooting) |

Este documento explica **o quê** foi escolhido e **por quê**, com as alternativas consideradas e o que
cada escolha custa. Uma regra guiou a escrita: toda decisão precisa caber em uma frase que um avaliador
consiga verificar.

## 1. Visão geral

### 1.1 O que foi entregue, uma frase por parte

- **Parte 1.** Um serviço HTTP em Go que responde `GET /projeto-korp` com o horário UTC resolvido a cada
  requisição, empacotado numa imagem Alpine não-root de poucos MB, atrás de um NGINX oficial na porta 80,
  numa rede bridge fechada onde o app não publica porta nenhuma.
- **Parte 2.** Métricas no padrão Prometheus (disponibilidade e volume, mais latência e erros), Prometheus
  e Grafana provisionados por arquivo, dashboards versionados, regras de alerta com teste, e um punhado de
  exporters num perfil opcional do Compose.
- **Parte 3.** Um playbook Ansible com seis roles que instala o Docker, cria a rede, constrói a imagem,
  sobe o Compose, valida o NGINX e o monitoramento, e termina exibindo a resposta do serviço. Um comando,
  idempotente.

### 1.2 Arquitetura

```
cliente ──:80──▶ nginx (imagem oficial) ──korp-net──▶ http-server-projeto-korp:8080
                                                          │
                     prometheus:9090 ◀──scrape /metrics───┘
                          │
                     grafana:3000 (dashboards provisionados)
```

Perfil padrão do Compose: exatamente os quatro serviços acima. Perfil `full`: mais cinco componentes
opcionais (§6). A separação existe para que o comando avaliado tenha o menor número possível de peças que
podem falhar.

### 1.3 Princípios que guiaram as decisões

1. **Biblioteca padrão primeiro.** Dependência só entra quando resolve um problema que a stdlib não
   resolve. Cada exceção está declarada neste documento.
2. **Observabilidade por desenho.** O serviço nasce com métricas, logs estruturados e health checks,
   não os ganha depois.
3. **Falhar cedo.** Configuração inválida derruba o processo no boot, com mensagem, em vez de produzir
   comportamento estranho em produção.
4. **Rede fechada por padrão.** Só o proxy fala com o host.
5. **Tudo como código, tudo verificável.** Dashboards, regras de alerta, SLO e provisionamento são
   arquivos versionados com teste.

## 2. Ferramentas e bibliotecas — o quê, versão e por quê

| Ferramenta / biblioteca | Versão | Onde é usada | Por que esta, e não a alternativa |
|---|---|---|---|
| Go | 1.24 | serviço | Binário estático sem dependência de libc; `net/http` com padrões de método desde a 1.22; `log/slog` na stdlib. |
| `net/http` + `http.ServeMux` | stdlib | roteamento e servidor | Quatro rotas não justificam framework. `chi` e `gin` foram considerados: adicionam dependência sem ganho aqui. Middleware é `func(http.Handler) http.Handler` puro. |
| `log/slog` | stdlib | logs JSON | Zero dependência, JSON nativo, handler customizado injeta `trace_id` e `request_id`. `zap` e `zerolog` são mais rápidos, mas o ganho é irrelevante neste volume e o custo é uma API própria. |
| `github.com/prometheus/client_golang` | ver `go.mod` | `/metrics` | É literalmente "o padrão do Prometheus" que o brief pede: controle total de nomes, buckets e exemplars. OTel Metrics com exporter Prometheus reescreve nomes e muda a semântica do histograma. |
| `github.com/sethvargo/go-envconfig` | ver `go.mod` | configuração por env | Struct com tags, defaults e validação explícita no boot. `viper` traz `mapstructure`, `fsnotify` e precedência implícita; `koanf` resolve múltiplas fontes, problema que não existe aqui. |
| `github.com/swaggo/swag` + `http-swagger` | ver `go.mod` | documentação da API em `/swagger/` | Padrão de fato em Go: anotações no handler geram a spec, e o CI falha se ela estiver desatualizada. Alternativa contrato-primeiro com `oapi-codegen` geraria mais código que o serviço inteiro. |
| Docker Engine + Compose v2 | 27+ / v2 | build e execução | Exigidos pelo brief. |
| Imagem de build | `golang:1.24` | Dockerfile, estágio 1 | Toolchain oficial; o binário sai estático com `CGO_ENABLED=0`. |
| Imagem de runtime | `alpine:3.20` | Dockerfile, estágio 2 | Ver §3.2: shell para diagnóstico em campo, ao custo de uma superfície um pouco maior que a do distroless. |
| `nginx` | `nginx:1.27-alpine` | proxy reverso | Exigido pelo brief como imagem oficial; a variante Alpine é a mesma distribuição oficial, menor. |
| `prom/prometheus` | `v3.5.0` | coleta e regras | Série 3.x atual; `promtool` da mesma imagem valida config e regras no CI. |
| `grafana/grafana` | `11.3.0` | visualização | Provisioning de datasources e dashboards por arquivo, que é o diferencial citado pelo desafio. |
| `ansible-core` + `community.docker` | 2.17+ / 4.x | provisionamento | Módulos declarativos com idempotência real e suporte a `--check`. `shell: docker …` sempre reporta mudança e esconde erro. |
| `golangci-lint`, `gosec`, `govulncheck` | fixados no CI | qualidade e segurança do Go | Lint, análise estática de segurança e vulnerabilidades conhecidas nas dependências. |
| `hadolint`, `trivy`, `gitleaks` | fixados no CI | imagem, dependências, segredos | Cada um cobre uma camada: Dockerfile, CVEs, segredo vazado no histórico. |
| `promtool`, `amtool`, `ansible-lint`, `yamllint`, `actionlint` | fixados no CI | configs como código | Tudo que é YAML de infraestrutura tem um validador rodando antes do merge. |

## 3. Parte 1 — Serviço e arquitetura do ambiente

### 3.1 Serviço HTTP em Go (a)

| Pergunta | Resposta |
|---|---|
| O que fiz | `cmd/http-server-projeto-korp` monta config, servidor e sinais. `internal/handler` serializa `{"nome","horario"}` com `time.Now.UTC` chamado dentro do handler, a cada requisição, em RFC 3339. Rotas `/healthz`, `/readyz`, `/metrics`, `/swagger/`. `http.Server` com todos os timeouts explícitos e desligamento gracioso por `SIGTERM`. |
| Por quê | A stdlib basta para essas rotas. UTC dentro do handler porque o brief exige "resolvido dinamicamente a cada requisição"; RFC 3339 é o formato padrão de `time.Time` em JSON e não depende do fuso do container. |
| Alternativas consideradas | Framework HTTP (dependência sem ganho); `time.Now` sem `.UTC` (viola o brief se o `TZ` do container mudar); horário calculado no boot (viola "a cada requisição"). |
| Trade-offs | Sem framework, o middleware de métricas, logs e recuperação de panic é escrito à mão: mais código, menos dependência. |
| Como validar | `go test -race ./...` cobre handler com relógio injetado, 405 para métodos errados e o caso de duas requisições seguidas devolverem horários diferentes. Pela borda: `curl -s localhost/projeto-korp` duas vezes. |
| Clean code aplicado | Um comentário é uma frase que explica o porquê; godoc em todo identificador exportado; nenhum número mágico; todo erro envolvido com contexto; sem variável global mutável; handler recebe o relógio e as métricas por injeção. |

### 3.2 Dockerfile — build e execução em container

| Pergunta | Resposta |
|---|---|
| O que fiz | Multi-stage: `golang:1.24` compila com `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=…"`; runtime `alpine:3.20` com `ca-certificates` e `tzdata`, usuário `65532` criado no Dockerfile, `HEALTHCHECK` com o `wget` do BusyBox, `ENTRYPOINT` em forma exec. |
| Por quê | Alpine dá shell e `apk` para diagnosticar um container em campo sem rebuild. O binário é estático, então a `musl` do Alpine nem é usada pelo app, o que anula a objeção clássica ao Alpine em Go. |
| Alternativas consideradas | `distroless/static` (superfície mínima, mas sem shell: diagnóstico só por observabilidade); `distroless:debug` (tem shell, não tem gerenciador de pacotes, e uma tag "debug" em produção é difícil de defender); `scratch` (exige copiar CA, tzdata e `/etc/passwd` à mão). |
| Trade-offs | `apk`, `busybox` e `musl` entram no inventário de CVEs. Mitigado por usuário não-root, base pinada, `apk --no-cache`, `read_only` no Compose e `trivy` no CI. A pontuação entre Alpine e distroless ficou apertada; a escolha foi deliberada, não folgada. |
| Como validar | `hadolint Dockerfile` limpo; `docker build`; `docker image inspect --format '{{.Config.User}} {{.Size}}'` mostra usuário numérico e imagem abaixo de 30 MB; `trivy image`. |
| O que faria com mais tempo | Digest da base pinado no `FROM`; assinatura com `cosign`; SBOM publicado no release. |

### 3.3 Docker em ambiente Linux

| Pergunta | Resposta |
|---|---|
| O que fiz | Role `docker`: repositório APT oficial com chave em `/etc/apt/keyrings`, pacotes `docker-ce`, `docker-ce-cli`, `containerd.io`, `docker-buildx-plugin`, `docker-compose-plugin`; serviço habilitado; usuário no grupo `docker`. |
| Por quê | Repositório oficial entrega versão atual e assinada; automatizado, a mesma etapa serve à Parte 3. |
| Alternativas consideradas | `apt install docker.io` (versão antiga do Ubuntu); `curl get.docker.com \| sh` (não idempotente, não auditável). |
| Trade-offs | WSL2 exige `systemd=true` em `/etc/wsl.conf` para o serviço subir; documentado no README. |
| Como validar | `docker version`; `systemctl is-active docker`; `id -nG \| grep -w docker`. |

### 3.4 Rede Docker bridge

Rede `korp-net`, bridge, declarada com `name:` fixo no Compose e criada pela role `network` com o mesmo
nome. O app é alcançado pelo nome DNS `http-server-projeto-korp`, nunca por IP. Armadilha evitada: dentro
do container do NGINX, `localhost` é o próprio NGINX, então o `proxy_pass` usa o nome do serviço.

### 3.5 Docker Compose — app + nginx (a)

| Pergunta | Resposta |
|---|---|
| O que fiz | Serviço `http-server-projeto-korp` com `expose: 8080` e **sem `ports:`**; `nginx` com `80:80` e volume `./nginx/conf.d:/etc/nginx/conf.d:ro`; ambos em `korp-net`. Endurecimento em todos: `read_only`, `cap_drop: [ALL]`, `no-new-privileges`, `pids_limit`, e limites de CPU e memória. |
| Por quê | O brief exige que o app não publique porta e que o NGINX seja a única entrada. Limites de recurso existem porque sem quota as métricas de saturação de container não significam nada (§6). |
| Alternativas consideradas | Publicar `8080` "para facilitar o teste" (viola o brief e esconde defeito de proxy). |
| Trade-offs | `read_only` impede `apk add` dentro do container em execução; instalar ferramenta exige subir o container sem essa flag, de propósito. |
| Como validar | `docker compose config -q` nos dois perfis; `docker compose ps` mostra só `80`, `9090` e `3000` publicados. |

### 3.6 NGINX — proxy reverso (a)

Arquivo `nginx/conf.d/http-server-projeto-korp.conf`, com o nome exigido, montado somente leitura.
`upstream` apontando para `http-server-projeto-korp:8080`, cabeçalhos `X-Forwarded-*`, timeouts
explícitos, log de acesso em JSON. `/metrics` e `/debug/` devolvem 404 na borda: métricas são internas.
Uma `location /swagger/` expõe a documentação da API pela mesma porta 80.

Um defeito real foi encontrado e corrigido ao validar: `nginx -t` num container isolado falha porque o
NGINX resolve o nome do `upstream` na carga. A validação precisa rodar na rede `korp-net` com o app no ar,
e é assim que a role `nginx` faz.

### 3.7 Teste do ambiente

`curl http://localhost/projeto-korp` pela porta 80, e o mesmo teste feito pela role `validate`, que
exibe a resposta no console ao fim do playbook.

## 4. Parte 2 — Monitoramento e observabilidade

### 4.1 Métricas obrigatórias: disponibilidade e volume (a)

| Pergunta | Resposta |
|---|---|
| O que fiz | Volume: `http_requests_total{method,route,code}`. Disponibilidade, de três formas complementares: gauge `service_up` (1 após o boot, 0 ao iniciar a drenagem), endpoint `/healthz`, e o `up` do próprio scrape. Mais `http_request_duration_seconds` (histograma), `http_requests_in_flight` e `korp_build_info`. |
| Por quê | Cada forma de disponibilidade cobre uma falha diferente: processo, aplicação, coleta. O histograma permite p95 e SLO de latência. |
| Cardinalidade | `route` é o padrão da rota, nunca o path bruto; nada de IP ou identificador único em label. O scrape tem `sample_limit`, então estourar o orçamento derruba o alvo e dispara alerta, que é o detector mais barato que existe. |
| Como validar | `curl -s localhost:8080/metrics \| promtool check metrics`; painel de volume e de disponibilidade no dashboard. |

### 4.2 Prometheus

Job `http-server-projeto-korp` em `prometheus.yml`, com limites de scrape declarados. Regras de gravação
com a convenção `nível:métrica:operação`, SLO de disponibilidade e latência com alertas por taxa de queima
em múltiplas janelas, e testes unitários das regras com `promtool test rules`, executados no CI. Retenção
de 45 dias, porque a janela do orçamento de erro é de 30 e reter menos tornaria o painel mentiroso.

### 4.3 Grafana e dashboard (,,; bônus)

Datasources e dashboards provisionados por arquivo, sem clique. Dashboard `http-server-projeto-korp` com
disponibilidade, volume, erro e latência, mais `korp-container-health` (saúde de container e host) e
`korp-edge` (o que o usuário vê na borda). Todo painel tem unidade e descrição. Os dashboards são JSON
versionado e validado.

## 5. Parte 3 — Automação com Ansible

### 5.1 Estrutura do playbook e comando único

```
ansible-playbook -i ansible/inventory.ini ansible/playbook.yml
```

Seis roles, na ordem dos itens do brief: `docker → network → app → nginx → monitoring → validate`. Cada
uma tem `defaults/` com variáveis prefixadas, e o comando continua único. `any_errors_fatal` garante que
uma falha pare tudo com mensagem, em vez de deixar o ambiente pela metade.

### 5.2 a 5.7 As roles, uma frase cada

- **`docker`**: instala o Engine pelo repositório oficial, idempotente.
- **`network`**: cria `korp-net` com `community.docker.docker_network`.
- **`app`**: constrói a imagem e sobe o Compose com `docker_compose_v2`, aceitando
  `compose_profiles` para o perfil `full`.
- **`nginx`**: gera a configuração a partir de template, valida com `nginx -t` **na rede** e
  recarrega por handler.
- **`monitoring`**: valida `prometheus.yml` e todas as regras com o `promtool` da imagem oficial,
  espera Prometheus e Grafana ficarem prontos e confirma que o alvo do serviço está `up`.
- **`validate`**: faz a requisição pelo NGINX, assevera o JSON e o exibe com `debug`.

### 5.8 Idempotência e segurança do playbook

Módulos declarativos em vez de `shell`; onde `command` é inevitável, há `changed_when` explícito. A
segunda execução termina com `changed=0`. Nenhum segredo em variável: senha do Grafana vem de `.env`,
ignorado pelo git. O playbook foi validado neste ciclo com `yamllint`, `ansible-lint --profile production`
e `--syntax-check`; a execução real e a prova de idempotência dependem do alvo Linux (§8).

## 6. Além do mínimo, e por quê

O brief pede disponibilidade e volume. O que foi acrescentado responde a perguntas que essas duas métricas
deixam sem resposta.

- **O ponto cego do caminho do usuário.** O Prometheus coleta o serviço direto na rede interna e nunca
  passa pelo NGINX. Todo painel pode estar verde com o `curl` da porta 80 quebrado. Um `blackbox-exporter`
  sonda `http://nginx/projeto-korp` e verifica o contrato do JSON, não só o código 200. É a quarta forma
  de disponibilidade, e a única que enxerga a borda.
- **Saúde de container e host.** cAdvisor e node-exporter, com limites de recurso declarados em todos os
  serviços, porque sem quota o throttling nunca ocorre e a razão de saturação vira infinito.
- **Saturação na borda.** O `nginx-exporter` lendo `stub_status` responde uma pergunta que nenhuma métrica
  do app responde: conexão aceita e descartada antes de chegar ao serviço.
- **Quem vigia o vigia.** Alertas para reload de configuração que falhou, regra que não avalia, e
  notificação que não sai, porque nesse caso todos os outros alertas ficam mudos.
- **Runbooks.** Cada alerta aponta para uma seção de `docs/runbooks/ALERT_RUNBOOKS.md` com mitigação antes
  de causa.

Tudo isso vive no perfil `full` do Compose, subido pelo mesmo playbook com uma variável a mais. O
avaliador vê primeiro o mínimo do desafio funcionando; o resto é opcional.

**O que deliberadamente ficou de fora.** Backend de traces, de logs e de perfis (Tempo, Loki, Alloy,
Pyroscope) e o OpenTelemetry Collector. Eles resolvem problemas reais, e eu opero esse conjunto em
produção, mas aqui custariam dez containers e um pipeline inteiro para um serviço de um endpoint. A
complexidade não se pagaria e multiplicaria o que pode falhar na demonstração. Sei explicar como cada
peça entra quando o volume justificar.

## 7. Como validar tudo

| O quê | Comando |
|---|---|
| Serviço, lint, testes, segurança, Swagger em dia | `make check` |
| Imagem e compose | `make docker`, `make up`, `make smoke` |
| Regras do Prometheus | `make promtool-test` |
| Playbook | `make ansible-check` e, no alvo Linux, `make ansible-run` |
| Tudo, no CI | `.github/workflows/ci.yml`, um job por área, ferramentas chamadas diretamente |

## 8. Limitações conhecidas e próximos passos

- A execução real do playbook e a prova de idempotência (`changed=0` na segunda execução) só podem ser
  feitas num alvo Linux. Lint, sintaxe e as expressões foram validados.
- Os digests das imagens base ainda não estão pinados no `FROM`; as tags são fixas.
- cAdvisor e node-exporter são frágeis em WSL2 (cgroup v2, mounts `9p`). Numa VM não há esse problema.
- Com mais tempo: TLS na borda, alerta de 5xx por rota a partir do access log do NGINX, teste de carga com
  k6 sobreposto ao painel do servidor.

## 9. Referências

- Brief do Desafio DevOps "Projeto Korp".
- Prometheus: convenções de nomes de métricas e regras de gravação.
- Google SRE Workbook: alertas por taxa de queima em múltiplas janelas.
- Docker: boas práticas de Dockerfile e do Compose.
- Ansible: `community.docker` e o guia de idempotência.
