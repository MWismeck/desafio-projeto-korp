---
title: Runbooks por Alerta
status: canonical
updated: 2026-09-06
owner: SRE/On-call (Architect cura)
consumers: [SRE/On-call, Incident Commander, Executor]
related: [../DECISOES_TECNICAS.md, ../../observability/prometheus/rules/]
---

# ALERT RUNBOOKS — um por alerta

> **Por que este documento existe:** toda regra de alerta em `observability/prometheus/rules/` aponta para
> cá via `runbook_url` (âncora = nome do alerta em minúsculas). Um alerta sem runbook é um alerta que
> acorda alguém sem dizer o que fazer; por isso cada seção tem mitigação antes de causa.

## Índice

| Alerta | Severidade | Sintoma | Regra |
|---|---|---|---|
| [ServiceHighErrorRateBurn](#servicehigherrorrateburn) | critical/warning | error budget de disponibilidade queimando | `observability/prometheus/rules/service.rules.yml` |
| [ServiceHighLatencyBurn](#servicehighlatencyburn) | critical/warning | error budget de latência queimando | idem |
| [TargetDown](#targetdown) | critical | Prometheus não consegue coletar o serviço | idem |
| [ServiceUnavailable](#serviceunavailable) | critical | serviço se declara indisponível (`service_up == 0`) | idem |
| [UserPathUnavailable](#userpathunavailable) | critical | app são, mas o caminho do usuário (via NGINX) não responde | `external.rules.yml` |
| [ServiceContractViolated](#servicecontractviolated) | critical | HTTP 200 com corpo fora do contrato da API | `external.rules.yml` |
| [ServiceExternalAvailabilityBurn](#serviceexternalavailabilityburn) | critical/warning | error budget de disponibilidade EXTERNA queimando | `external.rules.yml` |
| [ContainerOOMKilled](#containeroomkilled) | critical | o kernel matou o container por estouro de memória | `container.rules.yml` |
| [ContainerRestartLoop](#containerrestartloop) | critical | container reinicia em laço (visão do runtime) | `container.rules.yml` |
| [ServiceRestartLoop](#servicerestartloop) | critical | processo reinicia em laço (visão do processo, sem cAdvisor) | `container.rules.yml` |
| [ContainerCPUThrottled](#containercputhrottled) | warning | CFS estrangulando a CPU do container | `container.rules.yml` |
| [ContainerMemoryNearLimit](#containermemorynearlimit) | warning | working set encostando no limite de memória (pré-OOM) | `container.rules.yml` |
| [ContainerMissing](#containermissing) | warning | container sumiu do cAdvisor | `container.rules.yml` |
| [NginxConnectionsSaturated](#nginxconnectionssaturated) | warning | NGINX aceita e não atende: conexões descartadas na borda | `platform.rules.yml` |
| [ProcessFileDescriptorsNearLimit](#processfiledescriptorsnearlimit) | warning | descritores de arquivo perto do teto | `platform.rules.yml` |
| [PrometheusConfigReloadFailed](#prometheusconfigreloadfailed) | critical | Prometheus rodando com configuração antiga | `platform.rules.yml` |
| [PrometheusRuleEvaluationFailing](#prometheusruleevaluationfailing) | critical | recording rules e alertas parados (SLI congelado) | `platform.rules.yml` |
| [AlertmanagerNotificationsFailing](#alertmanagernotificationsfailing) | critical | alerta dispara e ninguém é avisado | `platform.rules.yml` |
| [PrometheusHeadSeriesHigh](#prometheusheadserieshigh) | warning | cardinalidade acima do orçamento | `platform.rules.yml` |
| [HostDiskWillFill](#hostdiskwillfill) | warning | disco do host previsto para acabar em menos de 4 h | `platform.rules.yml` |

Todos os arquivos de regra ficam em `observability/prometheus/rules/`.

Formato canônico por alerta: **Significado · Impacto · Como confirmar · Mitigação · Confirmar resolução ·
Causas prováveis · Escalonamento**. Ordem das mitigações: **mais rápida e reversível primeiro**.

Comandos usados abaixo assumem o alvo Linux, na raiz do projeto, com a porta do Prometheus publicada em
`127.0.0.1:9090`. Consulta pontual:

```bash
curl -sG http://localhost:9090/api/v1/query --data-urlencode 'query=<EXPR>' | jq '.data.result'
curl -s http://localhost:9090/api/v1/alerts | jq '.data.alerts[].labels'
amtool --alertmanager.url=http://localhost:9093 alert query      # só no perfil full
```

---

## ServiceHighErrorRateBurn

**Significado.** A taxa de respostas 5xx consome o error budget de disponibilidade (SLO 99,9 %/30 d) mais
rápido que o permitido: 14,4× (1h∧5m, page), 6× (6h∧30m, page) ou 1× (3d∧6h, ticket).

**Impacto.** Usuários recebem erro em `GET /projeto-korp`. A 14,4×, 2 % do budget mensal some em 1 h.

**Como confirmar.**
```promql
sum by (code) (rate(http_requests_total{service="http-server-projeto-korp"}[5m]))
1 - service:sli_availability:ratio_rate1h
```
Dashboard `http-server-projeto-korp` → painel "Taxa de erro"; `slo-overview` → burn rate.

**Mitigação.** Erro começou após deploy → rollback para a tag anterior. NGINX devolvendo 502/504
→ ver [ServiceUnavailable](#serviceunavailable). Erro em rota específica → desabilitar/feature flag.

**Confirmar resolução.** `1 - service:sli_availability:ratio_rate5m` abaixo de `0.0144` e caindo; o alerta
sai de `http://localhost:9090/alerts` em poucos minutos — a janela curta existe para resolver rápido.

**Causas prováveis.** Deploy com bug; dependência externa fora; saturação (CPU/memória → timeouts);
config inválida no boot (app reiniciando em loop: `docker compose ps`).

**Escalonamento.** `critical` → SEV2; se 100 % de erro → SEV1.

---

## ServiceHighLatencyBurn

**Significado.** A fração de requisições acima de 300 ms consome o error budget de latência nas mesmas
janelas multiwindow do alerta de erro.

**Impacto.** Serviço lento; risco de timeouts em cascata no NGINX (`proxy_read_timeout 30s`).

**Como confirmar.**
```promql
histogram_quantile(0.95, sum by (le) (rate(http_request_duration_seconds_bucket{service="http-server-projeto-korp"}[5m])))
service:sli_latency_good:ratio_rate1h
```
Painel "Latência p50/p95/p99" e "Saturação" (goroutines, memória, CPU).

**Mitigação.** Saturação → escalar/limitar concorrência; após deploy → rollback; GC/memória → verificar
`GOMEMLIMIT` e limites do container.

**Confirmar resolução.** `service:sli_latency_good:ratio_rate5m` de volta acima de `0.99` e p95 abaixo de
300 ms no painel de latência.

**Causas prováveis.** Lock contention; dependência lenta; CPU throttling; log síncrono excessivo.

**Escalonamento.** `critical` → SEV2; se p95 > timeout do NGINX → tratar como erro (SEV1/2).

---

## TargetDown

**Significado.** `up{job="http-server-projeto-korp"} == 0` por 1 min: o Prometheus não consegue raspar
`/metrics`. Não prova que o serviço está fora — prova que **ninguém enxerga** o serviço.

**Impacto.** Cegueira operacional; todos os outros alertas do serviço silenciam (sem dados).

**Como confirmar.** `http://localhost:9090/targets` (erro do scrape: DNS, conexão recusada, 404);
`docker compose exec prometheus wget -qO- http://http-server-projeto-korp:8080/metrics | head`.

**Mitigação.** Container parado → `docker compose up -d http-server-projeto-korp`; fora da rede
`korp-net` → corrigir `networks:` no compose; porta/path errados → `prometheus.yml`
(`http-server-projeto-korp:8080`, `/metrics`) + reload (`curl -X POST localhost:9090/-/reload`).

**Confirmar resolução.** `up{job="http-server-projeto-korp"}` = 1 em dois scrapes seguidos (30 s) e o alvo
verde em `/targets`, com `lastError` vazio.

**Causas prováveis.** App em crash loop (config inválida); rede recriada; nome do serviço alterado;
`/metrics` movido para porta interna sem atualizar o scrape.

**Escalonamento.** Se `curl localhost/projeto-korp` também falha → [ServiceUnavailable](#serviceunavailable)/SEV1.

---

## ServiceUnavailable

**Significado.** O próprio serviço reporta `service_up == 0` por 2 min (readiness falhou: dependência
indisponível ou shutdown em curso) — sinal de disponibilidade exigido pelo desafio.

**Impacto.** Requisições falham ou o NGINX devolve 502. SEV1 se persistente.

**Como confirmar.** `docker compose exec nginx wget -qO- http://http-server-projeto-korp:8080/readyz`;
logs JSON do app (`docker compose logs --tail=100 http-server-projeto-korp`) com `msg` de readiness;
`curl -s -o /dev/null -w '%{http_code}' http://localhost/projeto-korp`.

**Mitigação.** Reiniciar o container; se após deploy, rollback; dependência fora → mitigar a dependência
ou degradar graciosamente (feature flag).

**Confirmar resolução.** `service_up{job="http-server-projeto-korp"}` = 1 por 2 min seguidos e
`curl -s -o /dev/null -w '%{http_code}' http://localhost/projeto-korp` devolvendo `200`.

**Causas prováveis.** Dependência externa fora; graceful shutdown travado (drain maior que o
`stop_grace_period`); disco/porta ocupada.

**Escalonamento.** SEV1 imediato se `curl` via NGINX ≠ 200 por > 5 min.

---

## UserPathUnavailable

**Significado.** A sonda do blackbox **através do NGINX** falhou (`probe_success == 0`) enquanto o app se
declara pronto (`service_up == 1`): o processo está são e **o usuário não chega até ele** — o defeito está
na borda (NGINX, `proxy_pass`, rede `korp-net`), não no app.

**Impacto.** `curl http://localhost/projeto-korp` falha para **todos** os usuários. Este é
o cenário que `TargetDown`, `ServiceUnavailable` e o burn interno de 5xx **não** enxergam: o Prometheus
raspa o app direto na `korp-net`, na porta 8080, sem passar pelo proxy — o dashboard RED fica verde
enquanto o serviço está, na prática, fora. Queima o error budget **externo** a 1000× (SLI externo em 0)
sem tocar no interno.

**Como confirmar.** Começar pelo caminho do usuário e descer até a rede:
```bash
curl -s -o /dev/null -w '%{http_code}\n' http://localhost/projeto-korp   # esperado 200
docker compose ps nginx http-server-projeto-korp                         # estado e saúde
docker compose exec nginx nginx -t                                       # a conf em uso é válida?
docker compose logs --since 15m nginx | grep -E "upstream|host not found| 50[0-9] "
docker network inspect korp-net --format "{{range.Containers}}{{.Name}} {{end}}"
docker compose exec nginx wget -qO- http://http-server-projeto-korp:8080/projeto-korp | head -c 200
docker compose exec prometheus wget -qO- "http://blackbox-exporter:9115/probe?module=korp_contract&target=http://nginx/projeto-korp" | grep -E "^probe_success|^probe_http_status_code|^probe_duration_seconds"
```
Leitura: o `wget` **de dentro do container do NGINX** chegando ao app prova que rede e DNS estão bons e
que o defeito é de configuração do proxy; falhando, o defeito é de rede, DNS ou do próprio container.

**Mitigação.** Nesta ordem (mais rápida e reversível primeiro) — **tudo na borda, não no app**:

| # | Situação | Ação |
|---|---|---|
| 1 | `nginx -t` reprova | restaurar a conf boa (`git checkout -- nginx/conf.d/http-server-projeto-korp.conf`) e `docker compose exec nginx nginx -s reload` |
| 2 | `nginx -t` passa mas o proxy erra | corrigir para `proxy_pass http://http-server-projeto-korp:8080;` (**nunca** `localhost`/`127.0.0.1`, que dentro do container do NGINX é o próprio NGINX) e recarregar |
| 3 | container `nginx` parado ou reiniciando | `docker compose up -d nginx`; se persistir, `docker compose up -d --force-recreate nginx` |
| 4 | `nginx` fora da `korp-net` (rede recriada por fora do compose) | `docker compose up -d` (reconecta) e conferir com `docker network inspect korp-net` |
| 5 | app inalcançável de dentro do `nginx` (DNS do compose não resolve) | recriar os dois, nesta ordem: `docker compose up -d --force-recreate http-server-projeto-korp nginx` |
| 6 | nada acima resolve em 10 min | abrir incidente. **Não** publicar a porta 8080 do app como "solução": contorna o caminho real do usuário e esconde o defeito da borda |

**Confirmar resolução.** `curl -s http://localhost/projeto-korp` devolve o JSON com HTTP 200;
`probe_success{job="blackbox-http",probe="contrato"}` = 1 por 1 min (o `for` da regra) e o alerta sai de
`http://localhost:9090/api/v1/alerts`. Conferir também `probe_duration_seconds` bem abaixo de 5 s: sonda
lenta volta a falhar sozinha (o módulo `korp_contract` tem `timeout: 5s`).

**Causas prováveis.** `proxy_pass` para `localhost`/`127.0.0.1` dentro do container do NGINX (C3);
`location` errada ou removida; reload com conf inválida (o NGINX segue servindo a conf antiga e o defeito
só aparece no próximo restart); container `nginx` morto, sem CPU ou morto por OOM; `korp-net` recriada sem
um dos containers; `proxy_read_timeout` estourando; app fazendo bind só em `127.0.0.1` dentro do próprio
container — neste último caso o `wget` de dentro do `nginx` também falha.

**Escalonamento.** SEV1 imediato mesmo com o app verde: para o usuário e para o avaliador, o serviço está
fora. Se `ServiceContractViolated` disparar junto, tratar como um único incidente de borda.

---

## ServiceContractViolated

**Significado.** A sonda recebeu **HTTP 200** mas o corpo não bate com a regex do módulo `korp_contract`
(`probe_failed_due_to_regex == 1`): a resposta não contém `"nome": "Projeto Korp"` — o contrato da
API está quebrado.

**Impacto.** O usuário (e o avaliador) recebe uma resposta **errada com cara de sucesso**. Nenhum SLI
baseado em 5xx registra isso: `http_requests_total{code="200"}`, `service_up`, `up` e o burn interno ficam
**todos verdes**. Este é o único alerta da stack que enxerga a violação do contrato.

**Como confirmar.**
```bash
curl -s http://localhost/projeto-korp | jq .                   # esperado {"nome":"Projeto Korp","horario":"..."}
curl -sI http://localhost/projeto-korp | grep -i content-type   # esperado application/json
curl -s http://localhost/projeto-korp; sleep 2; curl -s http://localhost/projeto-korp   # horario MUDA
docker compose exec prometheus wget -qO- "http://blackbox-exporter:9115/probe?module=korp_contract&target=http://nginx/projeto-korp&debug=true" | head -60
docker compose exec nginx wget -qO- http://http-server-projeto-korp:8080/projeto-korp   # o app por dentro
```
Leitura: app certo por dentro e errado pelo NGINX → defeito da borda (página própria do NGINX, `rewrite`
ou path errado). Errado nos dois → defeito do app.

**Mitigação.** Nesta ordem:

| # | Situação | Ação |
|---|---|---|
| 1 | mudou depois de um deploy | rollback para a tag anterior: `ansible-playbook -i ansible/inventory.ini ansible/playbook.yml -e app_version=<tag anterior>` ou `VERSION=<tag> docker compose up -d http-server-projeto-korp` |
| 2 | NGINX serve página própria (404 ou index padrão) em vez de fazer proxy | corrigir `location /projeto-korp` em `nginx/conf.d/http-server-projeto-korp.conf` e `docker compose exec nginx nginx -s reload` |
| 3 | `Content-Type` fora de `application/json` | corrigir o header no handler; até lá, registrar o desvio no incidente — a resposta continua errada para o cliente |
| 4 | corpo errado vindo do app (campo renomeado, `{}` em erro parcial) | rollback; sem versão anterior boa, correção por PR de emergência |

**Não silenciar sem PR de correção associado**: silenciar este alerta devolve exatamente o ponto cego que
a sonda externa existe para fechar.

**Confirmar resolução.** `probe_failed_due_to_regex{job="blackbox-http"}` = 0 e
`probe_success{probe="contrato"}` = 1; o comando
`curl -s http://localhost/projeto-korp | jq -e ".nome == \"Projeto Korp\" and (.horario|length) > 0"`
sai com código 0; dois `curl` seguidos devolvem `horario` diferente (o campo é resolvido a cada
requisição, não no build).

**Causas prováveis.** Campo renomeado (`nome` → `name`) ou serializado por outro encoder; handler
devolvendo `{}` ou erro parcial com status 200; NGINX servindo `index.html` ou o 404 padrão; `rewrite`
ou path errado no proxy; `horario` congelado no build (viola "UTC resolvido a cada requisição" do nome
canônico); resposta alterada por filtro/compressão no proxy.

**Escalonamento.** SEV2. SEV1 se ocorrer durante a janela de avaliação ou junto com
[ServiceExternalAvailabilityBurn](#serviceexternalavailabilityburn) — nesse caso o serviço entrega
resposta errada de forma sustentada.

---

## ServiceExternalAvailabilityBurn

**Significado.** O SLO de disponibilidade **externa** (sonda `korp_contract` através do NGINX,
99,9 %/30 d) está sendo consumido rápido demais: 14,4× (1h∧5m) ou 6× (6h∧30m) → page; 1× (3d∧6h) →
ticket. É o gêmeo externo de [ServiceHighErrorRateBurn](#servicehigherrorrateburn).

**Impacto.** Aqui entram **NGINX, rede e contrato**, não só o app. Este alerta pode queimar com o SLO
interno intacto — e é esse justamente o caso que importa: o processo está bem, o usuário não é atendido.
A 14,4×, 2 % do orçamento mensal externo (43,2 min em 30 d) some em 1 h.

**Como confirmar.**
```promql
slo:external_availability:burn_rate5m
slo:external_availability:burn_rate1h
service:sli_availability_external:ratio_avg5m      # 1 = todas as sondas OK
service_probe:probe_success:ratio_avg5m            # por sonda: contrato vs liveness
service_probe:probe_duration_seconds:avg5m         # sonda perto de 5 s vira falha
service:sli_availability:ratio_rate5m              # o SLI INTERNO, para comparar
```
Dashboards `slo-overview` (burn externo × interno) e `http-server-projeto-korp`. **A comparação entre a
linha externa e a interna é o diagnóstico**: as duas caindo → app; só a externa → borda.

**Mitigação.** Triagem em três vias, pela primeira que casar:

| Sinal | Onde está o defeito | Ação |
|---|---|---|
| [UserPathUnavailable](#userpathunavailable) também firing | borda | seguir aquele runbook (NGINX primeiro) |
| [ServiceContractViolated](#servicecontractviolated) também firing | contrato | seguir aquele runbook (rollback) |
| burn **interno** também firing | app | seguir [ServiceHighErrorRateBurn](#servicehigherrorrateburn) |
| nenhum dos três e `probe_duration_seconds` perto de 5 s | latência da borda | ver [NginxConnectionsSaturated](#nginxconnectionssaturated); subir `worker_connections` ou reduzir carga |
| só a sonda `liveness` falha | `/healthz` pelo proxy | conferir a `location /healthz` no NGINX |

Mitigação genérica enquanto a causa não aparece: `docker compose up -d --force-recreate nginx` (barato e
reversível) e, se houve deploy na última hora, rollback.

**Confirmar resolução.** `slo:external_availability:burn_rate5m` < 1 e
`service:sli_availability_external:ratio_avg5m` = 1 por pelo menos 5 min; o `critical` resolve em ≤ 2 min
depois disso (é para isso que serve a janela curta). O `warning` de 3d/6h só sai quando a janela longa
drena — **não** é sinal de que o problema continua.

**Causas prováveis.** NGINX reiniciando ou saturado; `proxy_pass`/`location` errados; contrato violado;
`timeout: 5s` do módulo `korp_contract` estourado por latência; `korp-net` instável; app realmente com
5xx (aí o burn interno acompanha).

**Ruído com poucas sondas** (exigência do padrão de alertas do projeto). Com
`scrape_interval` de 15 s a sonda roda 4×/min: 20 amostras em 5 min e 240 em 1 h. Uma única sonda falha em
5 min já dá razão de 5 % (burn 50×) — o page só ocorre porque a janela de 1 h também precisa passar de
1,44 %, ou seja ~4 sondas falhas na hora. Blip isolado **não** paga page; uma falha a cada 15 min, sim (e
deve mesmo paginar). Se aparecer ruído recorrente sem impacto, ajustar o intervalo da sonda — nunca o
`for`.

**Escalonamento.** SEV2; SEV1 se a razão externa ficar em 0 por > 5 min (equivale a serviço fora) ou se o
burn externo persistir com o interno saudável por > 30 min sem causa identificada.

---

## ContainerOOMKilled

**Significado.** `container_oom_events_total` subiu nos últimos 15 min: o kernel matou um processo do
cgroup por estourar o `mem_limit` do container (`for: 0m` — o kill já aconteceu, esperar não muda nada).

**Impacto.** Conexões em andamento são cortadas no meio (o cliente vê `connection reset`, o NGINX devolve
502), o container reinicia e há uma janela de indisponibilidade de segundos a dezenas de segundos. Queima
budget interno **e** externo ao mesmo tempo.

**Como confirmar.**
```bash
docker inspect http-server-projeto-korp --format "{{.State.OOMKilled}} {{.State.ExitCode}} {{.RestartCount}}"   # true / 137 / n
docker inspect http-server-projeto-korp --format "{{.HostConfig.Memory}}"                                       # limite em bytes
docker stats --no-stream
dmesg -T 2>/dev/null | grep -iE "out of memory|killed process" | tail -5
docker compose logs --tail=100 http-server-projeto-korp
```
```promql
container:oom_events:increase15m
container:memory_working_set:ratio        # quem está acima de 0.85 é o próximo
```

**Mitigação.** Nesta ordem:

| # | Situação | Ação |
|---|---|---|
| 1 | container não voltou (política `restart: unless-stopped` não reergueu) | `docker compose up -d http-server-projeto-korp` — restabelece o serviço agora |
| 2 | houve deploy na última hora | rollback para a tag anterior; a versão nova provavelmente aumentou o consumo |
| 3 | consumo legítimo maior que a quota | alívio imediato e reversível: subir `mem_limit` (ex.: `256m` → `512m`) e o `GOMEMLIMIT` correspondente (mantendo `GOMEMLIMIT` **abaixo** do `mem_limit`), depois `docker compose up -d http-server-projeto-korp` |
| 4 | padrão de crescimento contínuo (suspeita de vazamento) | **antes** de reiniciar de novo, guardar a evidência: `docker inspect` do container morto e a curva de `go_memstats_heap_inuse_bytes`/`go_goroutines` no painel "Saturação" — o reinício apaga o estado do processo |
| 5 | OOM repetido (> 2× em 30 min) | tratar como crashloop: [ContainerRestartLoop](#containerrestartloop) e abrir incidente |

**Confirmar resolução.** `container:oom_events:increase15m` = 0 e assim por 15 min inteiros (a janela da
regra), `container:memory_working_set:ratio` < 0,85, `docker inspect... {{.State.OOMKilled}}` = `false`
após a recriação e `docker compose ps` mostrando o container `Up` sem novos reinícios.

**Causas prováveis.** `mem_limit` apertado para a carga real; `GOMEMLIMIT` ausente ou acima do `mem_limit`
(o GC não é avisado e o kernel mata antes); vazamento de goroutine/buffer; corpo de requisição grande sem
limite; cache em memória sem teto; pico de concorrência.

**Escalonamento.** SEV2. SEV1 se o container não voltar em 2 min ou se o OOM se repetir em laço.

---

## ContainerRestartLoop

**Significado.** `container_start_time_seconds` mudou mais de 2× na última hora (visão do **runtime**, via
cAdvisor): o container está subindo e morrendo em laço.

**Impacto.** Cada reinício é uma janela de indisponibilidade (502 do NGINX durante o boot); os contadores
do app zeram, o que distorce `rate` e faz os SLIs oscilarem. Sem estabilizar, nenhum diagnóstico de
latência ou erro é confiável.

**Como confirmar.**
```bash
docker compose ps                                     # coluna STATUS mostra "Restarting"
docker inspect http-server-projeto-korp --format "{{.RestartCount}} {{.State.ExitCode}} {{.State.OOMKilled}} {{.State.Error}}"
docker compose logs --tail=200 http-server-projeto-korp    # a razão está nas últimas linhas antes de cada morte
docker events --since 30m --filter container=http-server-projeto-korp --filter event=die
```
Leitura do `ExitCode`: `137` = morto por OOM/SIGKILL → [ContainerOOMKilled](#containeroomkilled);
`1`/`2` = falha na inicialização (config inválida, porta ocupada, dependência ausente); `0` em laço =
o processo está terminando sozinho (comando errado no entrypoint).

**Mitigação.** Nesta ordem:

| # | Situação | Ação |
|---|---|---|
| 1 | reinício começou depois de um deploy | rollback para a tag anterior — para o laço imediatamente |
| 2 | config inválida no boot (fail-fast do app) | corrigir a variável no `.env`/inventário e `docker compose up -d http-server-projeto-korp` |
| 3 | `ExitCode 137` | tratar como OOM: subir `mem_limit`/`GOMEMLIMIT` (ver aquele runbook) |
| 4 | log some rápido demais para ler | congelar o laço para investigar: `docker update --restart=no http-server-projeto-korp` e depois `docker compose logs --tail=200`; **reverter** com `docker update --restart=unless-stopped` assim que capturar |
| 5 | causa não encontrada em 15 min | abrir incidente; o serviço está efetivamente instável |

**Confirmar resolução.** `docker compose ps` com o container `Up` estável por > 15 min, `RestartCount`
parado e `container:starts:changes1h` deixando de crescer. **Atenção:** o alerta só resolve quando a
janela de 1 h drenar os reinícios antigos — container estável há 20 min ainda pode manter o alerta firing;
isso é esperado, não é regressão.

**Causas prováveis.** Config inválida (fail-fast); OOM; porta já em uso; dependência indisponível no boot;
entrypoint/comando errado na imagem; healthcheck falhando com política de restart; volume/permissão
(usuário non-root sem acesso ao caminho montado).

**Escalonamento.** SEV2; SEV1 se o serviço estiver inalcançável pelo NGINX enquanto o laço dura.

---

## ServiceRestartLoop

**Significado.** `process_start_time_seconds` do app mudou mais de 2× na última hora (visão do
**processo**, pelas métricas do próprio serviço): o processo subiu de novo várias vezes. Vale no **perfil
padrão**, sem cAdvisor — é a rede de segurança quando `ContainerRestartLoop` não existe.

**Impacto.** Mesmo de [ContainerRestartLoop](#containerrestartloop): janelas de erro a cada reinício e
contadores zerados. Se os dois alertas disparam juntos, é **um** evento visto de dois ângulos — a inibição
do Alertmanager não os agrupa (nomes diferentes), então trate como um só no incidente.

**Como confirmar.**
```promql
instance:process_start_time_seconds:changes1h
time - process_start_time_seconds{job="http-server-projeto-korp"}    # uptime em segundos
changes(process_start_time_seconds{job="http-server-projeto-korp"}[15m])
```
```bash
docker compose ps http-server-projeto-korp
docker compose logs --tail=200 http-server-projeto-korp | tail -50
```

**Mitigação.** Idêntica à de [ContainerRestartLoop](#containerrestartloop): rollback se houve deploy;
corrigir config inválida; tratar OOM; congelar o laço com `docker update --restart=no` só para capturar o
log e reverter em seguida. Se a causa for **deploys legítimos** (3 execuções do playbook na mesma hora),
não há o que mitigar: silenciar por ≤ 2 h com autor, motivo e link do PR e registrar no log do turno.

**Confirmar resolução.** `time - process_start_time_seconds` crescendo continuamente e acima de 900 s
(15 min); `instance:process_start_time_seconds:changes1h` estável; o alerta sai quando a janela de 1 h
drenar.

**Causas prováveis.** Panic não recuperado no handler; OOM; config inválida; deploys sucessivos
(falso positivo conhecido); shutdown por sinal externo (orquestrador, `docker compose restart` em laço de
script).

**Escalonamento.** SEV2; SEV1 se acompanhado de `TargetDown`/`UserPathUnavailable`.

---

## ContainerCPUThrottled

**Significado.** Mais de 25 % do tempo executável do container foi retirado pelo escalonador CFS em 5 min
(`container:cpu_cfs_throttled:ratio_rate5m > 0.25`, sustentado por 10 min): a quota de `cpus:` é menor
que a demanda.

**Impacto.** É a causa clássica de **latência alta com "CPU baixa"** no painel: o processo fica pronto para
rodar e o kernel o segura. Costuma preceder `ServiceHighLatencyBurn`. Nenhum erro aparece — só espera.

**Como confirmar.**
```promql
container:cpu_cfs_throttled:ratio_rate5m
rate(container_cpu_usage_seconds_total{name="http-server-projeto-korp"}[5m])   # uso absoluto, em cores
service:sli_latency_good:ratio_rate5m                                          # a latência já sentiu?
```
```bash
docker inspect http-server-projeto-korp --format "{{.HostConfig.NanoCpus}} {{.HostConfig.CpuQuota}} {{.HostConfig.CpuPeriod}}"
docker stats --no-stream http-server-projeto-korp
```

**Mitigação.** Nesta ordem:

| # | Situação | Ação |
|---|---|---|
| 1 | latência do usuário já degradada | subir a quota: `cpus: "0.5"` → `"1.0"` no `compose.yml` e `docker compose up -d http-server-projeto-korp` (reversível, sem downtime perceptível) |
| 2 | pico de carga conhecido (teste `k6`, demo) | nenhuma ação; silenciar pela duração do teste, com motivo |
| 3 | subiu depois de um deploy | rollback: a versão nova passou a gastar mais CPU por requisição |
| 4 | throttling persiste com quota maior | comparar `rate(container_cpu_usage_seconds_total)` com a taxa de requisições para achar o custo de CPU por requisição e abrir item de backlog |

Sobre `GOMAXPROCS`: com quota fracionária (`cpus: "0.5"`), o runtime do Go pode assumir todos os núcleos
do host e piorar o throttling. Conferir se o serviço declara `GOMAXPROCS` coerente com a quota.

**Confirmar resolução.** `container:cpu_cfs_throttled:ratio_rate5m` abaixo de 0,25 por 10 min seguidos e
`service:sli_latency_good:ratio_rate5m` de volta acima de 0,99.

**Causas prováveis.** Quota de CPU pequena para a carga; `GOMAXPROCS` maior que a quota; trabalho pesado
por requisição (serialização, compressão, log síncrono); GC agressivo por `GOMEMLIMIT` apertado; ruído de
vizinhança no host.

**Escalonamento.** `warning` — ticket no próximo dia útil. Vira SEV2 se `ServiceHighLatencyBurn` disparar
junto e a mitigação 1 não resolver.

---

## ContainerMemoryNearLimit

**Significado.** `working_set / mem_limit` acima de 0,85 por 10 min: o container está encostando no
limite de memória. É o alerta **preventivo** do OOM.

**Impacto.** Ainda **nenhum** para o usuário — esse é o ponto. Se nada for feito, o desfecho provável é
[ContainerOOMKilled](#containeroomkilled), com conexões cortadas e reinício.

**Como confirmar.**
```promql
container:memory_working_set:ratio
container_memory_working_set_bytes{name="http-server-projeto-korp"}
container_spec_memory_limit_bytes{name="http-server-projeto-korp"}
predict_linear(container_memory_working_set_bytes{name="http-server-projeto-korp"}[1h], 3600)   # tendência
go_memstats_heap_inuse_bytes{job="http-server-projeto-korp"}                                    # heap do Go
```
Curva **plana e alta** = dimensionamento apertado. Curva **subindo sem parar** = vazamento.

**Mitigação.** Nesta ordem:

| # | Situação | Ação |
|---|---|---|
| 1 | curva subindo continuamente (vazamento) | registrar a curva de `go_memstats_heap_inuse_bytes` e `go_goroutines` **antes** de reiniciar, depois `docker compose up -d --force-recreate http-server-projeto-korp` para ganhar tempo |
| 2 | curva plana e alta (dimensionamento) | subir `mem_limit` e o `GOMEMLIMIT` proporcional; `docker compose up -d http-server-projeto-korp` |
| 3 | começou depois de um deploy | rollback |
| 4 | outro container é o autor (Prometheus, Grafana ou um exporter do perfil `full`) | reduzir retenção/limites daquele componente; a stack de observação nunca deve espremer o serviço |

**Confirmar resolução.** `container:memory_working_set:ratio` abaixo de 0,85 por 10 min e a tendência de
1 h horizontal ou descendente; nenhum evento novo em `container:oom_events:increase15m`.

**Causas prováveis.** `mem_limit` apertado; `GOMEMLIMIT` ausente/mal calibrado; vazamento; cache sem teto;
carga sazonal; working set inflado por page cache de arquivo montado.

**Escalonamento.** `warning` — ticket. Vira SEV2 na hora em que virar OOM.

---

## ContainerMissing

**Significado.** `container_last_seen` não é atualizado há mais de 120 s: o cAdvisor deixou de ver o
cgroup do container — ele foi parado, removido, ou o cAdvisor perdeu acesso ao runtime.

**Impacto.** Depende de **qual** container sumiu, e essa é a primeira pergunta:
- `http-server-projeto-korp` ou `nginx` → o serviço do desafio está fora; `TargetDown`,
  `ServiceUnavailable` e `UserPathUnavailable` são o page real deste caso (por isso aqui é `warning`);
- componente do perfil `full` (Alertmanager, cAdvisor, node-exporter, blackbox-exporter,
  nginx-exporter) → perda de observabilidade, não de serviço;
- **nenhum container sumiu de fato** → o problema é o cAdvisor, e toda a família de alertas de container
  está cega (falha silenciosa, a pior categoria).

**Como confirmar.**
```bash
docker ps -a --filter name=http-server-projeto-korp --format "table {{.Names}}\t{{.Status}}"
docker compose ps -a
docker compose logs --tail=50 cadvisor
curl -s http://localhost:9090/api/v1/targets | jq -r ".data.activeTargets[] | select(.labels.job==\"cadvisor\") | .health, .lastError"
```
```promql
container:last_seen_age:seconds
count(container_last_seen)     # zero ou muito baixo = o cAdvisor é o problema, não o container
```

**Mitigação.** Nesta ordem:

| # | Situação | Ação |
|---|---|---|
| 1 | o container sumido é do perfil padrão | `docker compose up -d <serviço>` — restabelecer o serviço vem antes de entender o motivo |
| 2 | container removido de propósito (`docker compose down`, poda) | nenhuma ação; silenciar por ≤ 2 h com motivo, ou remover a série antiga esperando a drenagem |
| 3 | `count(container_last_seen)` perto de zero | o cAdvisor caiu ou perdeu o runtime: `docker compose --profile full up -d cadvisor` e conferir os mounts (`/var/run/docker.sock`, `/sys`, rootfs) |
| 4 | cAdvisor instável no WSL2 (cgroup v2, mounts `9p`/`drvfs`) | limitação conhecida do ambiente: registrar e, se recorrente, silenciar a família de container no WSL2 com o motivo documentado |

**Confirmar resolução.** `container:last_seen_age:seconds` abaixo de 120 para os containers esperados e
`count(container_last_seen)` de volta ao número de containers do perfil ativo; `docker compose ps` sem
serviço faltando.

**Causas prováveis.** Container parado/removido; `docker compose down` de um perfil; poda
(`docker system prune`); cAdvisor caído, sem permissão ou com mounts errados; cgroup v2 no WSL2; renomeação
do container (a série antiga envelhece e a nova aparece com outro `name`).

**Escalonamento.** `warning`. Se o container ausente for do perfil padrão, o page correspondente
(`TargetDown`/`UserPathUnavailable`) é que comanda a severidade do incidente.

---

## NginxConnectionsSaturated

**Significado.** O NGINX **aceitou** mais conexões do que **atendeu**
(`accepted - handled > 0` por 5 min): conexões estão sendo descartadas na borda por falta de
`worker_connections` ou de descritores de arquivo.

**Impacto.** O usuário vê `connection reset` / conexão recusada — **não** um 5xx. Nenhum SLI baseado em
`http_requests_total` registra isso, porque a requisição nunca chega ao app. O SLI **externo**
(`probe_success`) enxerga; o interno não.

**Como confirmar.**
```promql
instance:nginx_connections_dropped:rate5m
nginx_connections_active{job="nginx"}
nginx_connections_waiting{job="nginx"}
rate(nginx_http_requests_total{job="nginx"}[5m])
service_probe:probe_success:ratio_avg5m
```
```bash
docker compose exec nginx wget -qO- http://127.0.0.1:8081/stub_status
docker compose exec nginx sh -c "ulimit -n"                     # teto de fd do processo
docker compose logs --since 15m nginx | grep -iE "worker_connections|too many open files"
```

**Mitigação.** Nesta ordem:

| # | Situação | Ação |
|---|---|---|
| 1 | descarte em andamento com impacto | `worker_connections` (1024, padrão da imagem) fica em `/etc/nginx/nginx.conf`, que não é montado aqui — só `nginx/conf.d/` é: montar uma cópia com o valor maior (ex.: 4096) no serviço `nginx` do `compose.yml` e `docker compose up -d nginx` |
| 2 | `too many open files` no log | subir `ulimits.nofile` do serviço `nginx` no `compose.yml` e `docker compose up -d nginx` (recria o container) |
| 3 | teste de carga em curso | nenhuma ação; silenciar pela duração do teste, com motivo e autor |
| 4 | upstream lento segurando conexão | atacar a latência do app ([ServiceHighLatencyBurn](#servicehighlatencyburn)); conexão presa é sintoma, não causa |

**Confirmar resolução.** `instance:nginx_connections_dropped:rate5m` = 0 por 5 min seguidos,
`nginx_connections_waiting` estável e `service_probe:probe_success:ratio_avg5m` = 1.

**Causas prováveis.** `worker_connections`/`worker_processes` baixos; `ulimit -n` do container apertado;
`keepalive` mal dimensionado com o upstream; upstream lento acumulando conexões ativas; teste de carga;
varredura/robô abrindo conexões sem completar handshake.

**Escalonamento.** `warning` — ticket. Vira SEV2 se `ServiceExternalAvailabilityBurn` ou
`UserPathUnavailable` dispararem junto: aí o descarte já está custando disponibilidade real.

---

## ProcessFileDescriptorsNearLimit

**Significado.** `process_open_fds / process_max_fds` acima de 0,8 por 10 min em algum alvo com o
*process collector* (app, Prometheus, Alertmanager, exporters).

**Impacto.** Ainda nenhum. Ao chegar a 1, o processo para de aceitar conexões e de abrir arquivos —
falha total e abrupta, geralmente com `accept4: too many open files` no log. Alerta preventivo.

**Como confirmar.**
```promql
topk(5, instance:process_open_fds:ratio)
process_open_fds{job="http-server-projeto-korp"}
process_max_fds{job="http-server-projeto-korp"}
predict_linear(process_open_fds{job="http-server-projeto-korp"}[1h], 3600)
```
```bash
docker compose exec http-server-projeto-korp sh -c "ls -1 /proc/1/fd | wc -l"     # se a imagem tiver shell
docker compose exec http-server-projeto-korp sh -c "ulimit -n"
```
Curva subindo sem parar = vazamento (resposta HTTP não fechada, arquivo não fechado, socket em
`CLOSE_WAIT`). Curva plana e alta = limite baixo demais.

**Mitigação.** Nesta ordem:

| # | Situação | Ação |
|---|---|---|
| 1 | razão > 0,95 e subindo (falha iminente) | reiniciar o processo para ganhar tempo: `docker compose up -d --force-recreate <serviço>` — mede-se em segundos, é reversível e evita a parada dura |
| 2 | limite baixo | subir `ulimits.nofile` no `compose.yml` e `docker compose up -d <serviço>` |
| 3 | vazamento suspeito | capturar `go_goroutines` e o perfil antes de reiniciar; abrir bug — reiniciar só adia |
| 4 | começou após deploy | rollback |

**Confirmar resolução.** `instance:process_open_fds:ratio` abaixo de 0,8 por 10 min e a tendência de 1 h
plana; `predict_linear(...[1h], 3600)` abaixo de `process_max_fds`.

**Causas prováveis.** `resp.Body` não fechado em cliente HTTP; `keepalive` com muitos upstreams; sockets
em `CLOSE_WAIT`; `LimitNOFILE`/`ulimits.nofile` baixo; muitos arquivos de log/WAL abertos (o TSDB do
Prometheus é o caso típico).

**Escalonamento.** `warning`. Se o alvo for o `http-server-projeto-korp` e a razão passar de 0,95, tratar
como SEV2 e mitigar já.

---

## PrometheusConfigReloadFailed

**Significado.** `prometheus_config_last_reload_successful == 0` por 5 min: o último `reload` falhou e o
processo continua rodando com a **configuração anterior**.

**Impacto.** Tudo parece normal e nada do que foi mudado está valendo: jobs novos não são raspados, regras
novas não são avaliadas, alertas novos não existem. É falha silenciosa — a stack fica **verde por
cegueira**. Se o reload trazia regras novas, nenhum desses alertas está ativo.

**Como confirmar.**
```bash
docker compose logs --tail=50 prometheus | grep -iE "error|reload|parse"
docker compose exec prometheus promtool check config /etc/prometheus/prometheus.yml
docker compose exec prometheus promtool check rules /etc/prometheus/rules/*.rules.yml
curl -s http://localhost:9090/api/v1/status/config | jq -r ".data.yaml" | head -30
```
```promql
prometheus_config_last_reload_successful
time - prometheus_config_last_reload_success_timestamp_seconds     # há quanto tempo sem reload bom
```

**Mitigação.** Nesta ordem:

| # | Situação | Ação |
|---|---|---|
| 1 | erro de sintaxe/semântica apontado pelo `promtool` | corrigir o arquivo e recarregar: `curl -X POST http://localhost:9090/-/reload` |
| 2 | não dá para corrigir agora | **reverter** o arquivo para a última versão boa (`git checkout -- observability/prometheus/`) e recarregar — restabelece a configuração conhecida em segundos |
| 3 | `job_name` duplicado entre `prometheus.yml` e `scrape_full.yml` (quando habilitado; erro "found multiple scrape configs") | remover a duplicata; é o modo de falha típico da separação em dois arquivos |
| 4 | reload não responde (`--web.enable-lifecycle` ausente) | reiniciar: `docker compose restart prometheus` — o processo sobe já com o config corrigido |

**Confirmar resolução.** `prometheus_config_last_reload_successful` = 1;
`curl -s http://localhost:9090/api/v1/status/config` refletindo a mudança esperada; `/targets` com os
jobs novos presentes; `/rules` listando os grupos novos com `health: ok`.

**Causas prováveis.** YAML inválido; `job_name` duplicado entre os arquivos do glob `scrape_*.yml`; arquivo
de regra referenciado e ausente; permissão de leitura no bind mount; `scrape_timeout` maior que
`scrape_interval`; arquivo escrito pela role do Ansible com template quebrado.

**Escalonamento.** `critical` → SEV2. SEV1 se ficar claro que alertas de disponibilidade estiveram
desativados durante um incidente em curso.

---

## PrometheusRuleEvaluationFailing

**Significado.** `prometheus_rule_evaluation_failures_total` está subindo há 10 min: um grupo de regras
não consegue ser avaliado.

**Impacto.** As recording rules param de produzir séries: **SLI e burn rate congelam** e os alertas que
dependem delas nunca disparam. Todos os painéis de SLO passam a mostrar dados velhos ou vazios. Como no
`PrometheusConfigReloadFailed`, o perigo é o falso verde.

**Como confirmar.**
```bash
curl -s http://localhost:9090/api/v1/rules | jq -r ".data.groups[] | select(any(.rules[]; .health != \"ok\")) | .name"
curl -s http://localhost:9090/api/v1/rules | jq -r ".data.groups[].rules[] | select(.health != \"ok\") | \"\(.name) \(.lastError)\""
docker compose logs --tail=100 prometheus | grep -iE "rule|evaluation"
```
```promql
instance:prometheus_rule_evaluation_failures:rate5m
prometheus_rule_group_last_duration_seconds > 15    # grupo mais lento que o evaluation_interval
```

**Mitigação.** Nesta ordem:

| # | Situação | Ação |
|---|---|---|
| 1 | a última mudança foi em `rules/*.rules.yml` | reverter o arquivo (`git checkout -- observability/prometheus/rules/`) e `curl -X POST http://localhost:9090/-/reload` — traz o SLI de volta em um ciclo |
| 2 | erro de expressão em um grupo específico | comentar o grupo defeituoso, recarregar, corrigir com calma no PR (`promtool check rules` + `promtool test rules` antes de voltar) |
| 3 | avaliação estourando o tempo (`prometheus_rule_group_last_duration_seconds` > `evaluation_interval`) | aumentar `interval:` do grupo ou reduzir a janela/cardinalidade da expressão |
| 4 | falha por falta de memória do Prometheus | subir `mem_limit` do container e/ou reduzir retenção; ver [ContainerMemoryNearLimit](#containermemorynearlimit) |

**Confirmar resolução.** `instance:prometheus_rule_evaluation_failures:rate5m` = 0 por 10 min; todos os
grupos com `health: ok` em `/api/v1/rules`; as séries `slo:*` e `service:sli_*` voltando a ter amostras
recentes (`timestamp` do resultado próximo de `time`).

**Causas prováveis.** Expressão inválida ou referenciando métrica inexistente; `label_replace` com regex
que não casa; grupo lento demais para o `evaluation_interval`; pressão de memória; regra que faz
`sum` sobre cardinalidade alta demais.

**Escalonamento.** `critical` → SEV2. Enquanto durar, tratar os SLIs como **não confiáveis** e usar a
sonda externa (`probe_success`) e `up` como verdade operacional.

---

## AlertmanagerNotificationsFailing

**Significado.** `alertmanager_notifications_failed_total` está subindo há 10 min em alguma integração: o
Alertmanager recebe o alerta e **não consegue entregar** a notificação.

**Impacto.** O pior modo de falha da cadeia de alertas: a regra dispara, o Alertmanager registra, e
**ninguém é avisado**. O sistema parece saudável do lado do Prometheus. Vale para page e para ticket.

**Como confirmar.**
```bash
amtool --alertmanager.url=http://localhost:9093 alert query
amtool --alertmanager.url=http://localhost:9093 config routes show
docker compose logs --tail=100 alertmanager | grep -iE "notify|error|dial|timeout"
curl -s http://localhost:9093/-/ready
```
```promql
instance:alertmanager_notifications_failed:rate5m
rate(alertmanager_notifications_total[5m])
alertmanager_notification_latency_seconds_count
```

**Mitigação.** Nesta ordem:

| # | Situação | Ação |
|---|---|---|
| 1 | notificação caindo agora com alerta `critical` ativo | **avisar o on-call por fora do canal quebrado** (mensagem direta/telefone) e registrar no log do turno: a mitigação primeira é humana |
| 2 | URL do receiver errada/inalcançável | corrigir `observability/alertmanager/alertmanager.yml`, validar com `amtool check-config` e `docker compose restart alertmanager` |
| 3 | segredo do webhook expirado | trocar o arquivo referenciado por `*_file` (nunca o segredo inline) e reiniciar |
| 4 | destino externo fora do ar | rotear temporariamente para um receiver alcançável, com a mudança registrada no incidente e revertida depois |

**Confirmar resolução.** `instance:alertmanager_notifications_failed:rate5m` = 0 por 10 min e
`rate(alertmanager_notifications_total[5m])` > 0 para a integração afetada (entregando de novo). Teste de
ponta a ponta: `amtool --alertmanager.url=http://localhost:9093 alert add alertname=TesteEntrega severity=warning service=http-server-projeto-korp`
e confirmar a chegada no canal; expirar o alerta de teste em seguida.

**Causas prováveis.** Receiver `blackhole`/placeholder ainda em uso (pendência conhecida: canal real a
definir); URL do webhook errada ou fora do ar; DNS/rede do container; TLS/proxy corporativo;
credencial expirada; rate limit do destino.

**Escalonamento.** `critical` → SEV2. Enquanto não entregar, o on-call acompanha `/alerts` do Prometheus
manualmente e o turno registra a degradação.

---

## PrometheusHeadSeriesHigh

**Significado.** `prometheus_tsdb_head_series` acima de 100 000 por 30 min — o orçamento de cardinalidade
adotado nesta stack. O perfil `full` gira em torno de 4 000 séries: passar de 100 000 significa
que **alguma label está explodindo**.

**Impacto.** Memória e CPU do Prometheus crescem com a cardinalidade; o desfecho é OOM do Prometheus —
isto é, perda de **toda** a observabilidade, inclusive dos alertas. Consultas e avaliação de regras ficam
lentas antes disso.

**Como confirmar.**
```promql
prometheus_tsdb_head_series
topk(10, count by (__name__)({__name__=~".+"}))          # quais métricas
topk(10, count by (job)({__name__=~".+"}))               # qual job
scrape_samples_scraped                                   # por alvo, contra o sample_limit
```
```bash
curl -s http://localhost:9090/api/v1/status/tsdb | jq ".data.seriesCountByMetricName[:10]"
curl -s http://localhost:9090/api/v1/status/tsdb | jq ".data.labelValueCountByLabelName[:10]"
```
A segunda consulta responde a pergunta certa: **qual label** tem valores demais (`route` com id na URL,
`instance` por réplica efêmera, identificador de requisição virando label).

**Mitigação.** Nesta ordem:

| # | Situação | Ação |
|---|---|---|
| 1 | Prometheus já sob pressão de memória | conter agora com `metric_relabel_configs` (`action: drop`) no job culpado e `curl -X POST http://localhost:9090/-/reload` — curativo, mas imediato |
| 2 | job novo do perfil `full` estourando o `sample_limit` | ajustar o `sample_limit` do job **e** dropar as famílias de métrica sem painel/alerta (o padrão já existente para `cadvisor` e `node`) |
| 3 | label de alta cardinalidade vinda do app | corrigir **na origem** (rota normalizada, `id` fora da label) — PR no serviço; o relabel é paliativo |
| 4 | crescimento sem culpado claro | reduzir retenção (`--storage.tsdb.retention.time`) temporariamente para aliviar memória, registrando o desvio |

**Confirmar resolução.** `prometheus_tsdb_head_series` abaixo de 100 000 e estável por 30 min;
`process_resident_memory_bytes{job="prometheus"}` estabilizado; nenhum alvo com
`scrape_samples_scraped` encostando no `sample_limit`.

**Causas prováveis.** Label de alta cardinalidade no app (path com id, `user_id`, identificador de
requisição); cAdvisor sem os `metric_relabel_configs` de contenção; muitos containers efêmeros criando
`name` novos; teste de carga com rotas geradas.

**Escalonamento.** `warning` — ticket. Vira SEV2 se o Prometheus começar a reiniciar (aí é
[ContainerOOMKilled](#containeroomkilled) no `prometheus`, e a stack inteira fica cega).

---

## HostDiskWillFill

**Significado.** Pela tendência das últimas 6 h (`predict_linear`), o espaço livre de um ponto de montagem
chega a zero em menos de 4 h, **e** já há menos de 30 % livre agora. As duas condições juntas evitam o
alarme por oscilação momentânea.

**Impacto.** Ainda nenhum, e é essa a razão de o alerta existir: disco cheio derruba o TSDB do Prometheus,
o log do Docker e o próprio serviço, tudo ao mesmo tempo e sem aviso. É a falha mais previsível que existe.

**Como confirmar.**
```promql
mountpoint:node_filesystem_avail_bytes:predict_linear4h
mountpoint:node_filesystem_avail:ratio
node_filesystem_avail_bytes{job="node"} / 1024 / 1024 / 1024     # GiB livres
```
```bash
df -h
docker system df -v | head -30
du -sh /var/lib/docker/volumes/* 2>/dev/null | sort -h | tail -10
docker compose exec prometheus du -sh /prometheus
```

**Mitigação.** Nesta ordem (todas reversíveis ou sem perda de serviço):

| # | Situação | Ação |
|---|---|---|
| 1 | espaço acabando em horas | podar o que é descartável: `docker image prune -af` e `docker builder prune -af` — costuma devolver a maior fatia, sem tocar em dado ativo |
| 2 | log do Docker crescendo | conferir o `logging` do compose (`json-file` com `max-size`/`max-file`); recriar o container recicla o arquivo de log |
| 3 | TSDB do Prometheus grande | reduzir `--storage.tsdb.retention.time`/`retention.size` e `docker compose up -d prometheus`; a retenção precisa continuar ≥ 30 d do SLO — se não der, o desvio vai para o incidente |
| 4 | volume `alertmanager-data` do perfil `full` crescido | parar o perfil e remover o volume descartável: `docker compose --profile full down` + `docker volume rm <vol>` |
| 5 | nada disso basta | ampliar o disco do host/VM; é a única solução definitiva |

Nunca apagar volume de dado ativo (`prometheus-data`, `grafana-data`) sem registrar no incidente: apagar o
TSDB apaga o histórico do error budget.

**Confirmar resolução.** `mountpoint:node_filesystem_avail:ratio` acima de 0,3 e
`mountpoint:node_filesystem_avail_bytes:predict_linear4h` positivo (a projeção deixa de cruzar o zero) por
30 min; `df -h` confirmando.

**Causas prováveis.** Imagens e camadas de build acumuladas; log de container sem rotação; TSDB com
retenção alta demais para o disco; volumes dos componentes do perfil `full`; core dumps; disco da
VM/WSL2 pequeno.

**Escalonamento.** `warning` — ticket, mas com prazo: se a projeção cair para menos de 1 h, tratar como
SEV2 e mitigar imediatamente. Disco cheio em host de observabilidade cega todos os outros alertas.
