# http-server-projeto-korp

Serviço HTTP em Go atrás de um NGINX, com métricas no padrão Prometheus, dashboard no Grafana e
provisionamento completo por um único comando Ansible.

```
GET http://localhost/projeto-korp
{"nome":"Projeto Korp","horario":"2026-09-07T16:07:54Z"}
```

O campo `horario` é resolvido em UTC a cada requisição.

## Como subir

Há dois caminhos. O primeiro é o provisionamento completo, que é o que o desafio pede. O segundo é útil
para desenvolver.

### Um único comando, do zero

Num host Linux (Ubuntu 24.04 em VM ou WSL2), o playbook instala o Docker, cria a rede, constrói a imagem,
sobe os containers, configura o NGINX e o monitoramento, e no final faz a requisição e mostra a resposta.

```bash
git clone https://github.com/MWismeck/desafio-projeto-korp.git
cd desafio-projeto-korp
ansible-galaxy collection install -r ansible/requirements.yml
ansible-playbook -i ansible/inventory.ini ansible/playbook.yml -K
```

O `-K` faz o Ansible pedir a senha do `sudo`. O playbook instala pacote e escreve em `/etc`, então
precisa de elevação. Se o seu usuário tem `sudo` sem senha, pode omitir.

Ao final, a última tarefa imprime o JSON do serviço. Rodar de novo termina com `changed=0`: o playbook é
idempotente.

**Pré-requisitos:** `ansible-core` 2.17 ou mais novo, e um usuário com `sudo` no alvo. Para desenvolver, Go 1.25 (o `go.mod` fixa a toolchain em 1.25.14, que o Go baixa sozinho se você tiver uma versão anterior). No WSL2, o Docker precisa de
`systemd=true` em `/etc/wsl.conf` para que o serviço suba.

### Só o Compose, para desenvolver

Com o Docker já instalado:

```bash
cp .env.example .env
make up
make smoke
```

## O que fica acessível

| Endereço | O que é |
|---|---|
| http://localhost/projeto-korp | o serviço, através do NGINX |
| http://localhost/swagger/index.html | documentação da API |
| http://localhost:9090 | Prometheus |
| http://localhost:3000 | Grafana, com o dashboard já provisionado |

O serviço **não publica porta no host**. Só o NGINX, na porta 80, e as interfaces do Prometheus e do
Grafana. Todos conversam pela rede bridge `korp-net`.

## Endpoints do serviço

| Rota | Para quê |
|---|---|
| `GET /projeto-korp` | o endpoint do desafio |
| `GET /healthz` | o processo está vivo |
| `GET /readyz` | pronto para receber tráfego; responde 503 durante a drenagem |
| `GET /metrics` | métricas no formato Prometheus |
| `GET /swagger/` | documentação da API |

Método errado devolve 405 com o cabeçalho `Allow`. Rota inexistente devolve 404. Ambos em JSON, no
formato `application/problem+json`.

## Métricas

| Métrica | O que responde |
|---|---|
| `http_requests_total{method,route,code}` | volume de requisições |
| `http_request_duration_seconds` | latência, em histograma |
| `service_up` | o serviço se declara disponível |
| `http_requests_in_flight` | requisições em andamento |
| `korp_build_info{version,commit,go_version}` | qual versão está no ar |

Disponibilidade é observada de três formas complementares, porque cada uma falha de um jeito diferente: a
métrica `service_up` vem do processo, o `/healthz` vem da aplicação, e o `up` do Prometheus vem da coleta.

O rótulo `route` é sempre o padrão da rota, nunca o caminho recebido. Isso evita explosão de séries.

## Componentes opcionais

O perfil `full` acrescenta cinco peças que não são exigidas pelo desafio:

```bash
# via Compose
make up-full

# ou pelo mesmo playbook
ansible-playbook -i ansible/inventory.ini ansible/playbook.yml -e '{"compose_profiles":["full"]}'
```

No perfil padrão o Grafana mostra **um** dashboard, o do serviço, com todos os painéis preenchidos. Os
dois painéis que leem estes exporters só são provisionados junto com eles, para nada abrir em branco.

| Componente | Responde a pergunta |
|---|---|
| `blackbox-exporter` | o usuário consegue chegar ao serviço **agora**, atravessando o NGINX? |
| `cadvisor` | o container está sendo estrangulado por CPU ou perto do limite de memória? |
| `node-exporter` | o host tem CPU, memória e disco? |
| `nginx-exporter` | a borda está descartando conexão antes de chegar ao serviço? |
| `alertmanager` | roteia os alertas |

O motivo de ficarem separados: o comando que provisiona o ambiente do desafio deve ter o menor número
possível de peças que podem falhar. Elas sobem depois, com o mínimo já provado.

## Alertas

As regras vivem em `observability/prometheus/rules/` e têm teste unitário executado com
`promtool test rules`, tanto localmente quanto no CI. Cada alerta aponta para uma seção de
[`docs/runbooks/ALERT_RUNBOOKS.md`](docs/runbooks/ALERT_RUNBOOKS.md), que traz mitigação antes de causa.

## Desenvolvimento

```bash
make help     # lista os alvos
make check    # lint, testes, segurança e Swagger em dia — o mesmo que o CI roda
make test     # testes com detector de corrida e cobertura
make docker   # constrói a imagem
```

O CI está em [`.github/workflows/ci.yml`](.github/workflows/ci.yml) e chama as ferramentas diretamente,
sem script intermediário: `golangci-lint`, `gosec`, `govulncheck`, `hadolint`, `trivy`, `gitleaks`,
`promtool`, `amtool`, `ansible-lint` e `actionlint`. Um job sobe o ambiente e faz a requisição pela porta
80, que é o mesmo teste que o desafio descreve.

## Estrutura

```
cmd/http-server-projeto-korp/   binário: configuração, sinais, flags
internal/                       config, handler, server, middleware, metrics, logging, problem
api/                            documentação OpenAPI, gerada a partir das anotações
nginx/conf.d/                   proxy reverso
observability/                  Prometheus, regras com teste, dashboards e provisionamento do Grafana
ansible/                        playbook e as seis roles
docs/                           decisões técnicas e runbooks
```

## Decisões técnicas

O porquê de cada escolha, com as alternativas consideradas e o que cada uma custa, está em
[`docs/DECISOES_TECNICAS.md`](docs/DECISOES_TECNICAS.md).

## Se algo falhar

| Sintoma | Onde olhar |
|---|---|
| `curl` na porta 80 falha | `docker compose logs nginx`; `docker compose exec nginx nginx -t` |
| serviço responde direto mas não pelo NGINX | `docker network inspect korp-net`; o `proxy_pass` usa o nome do serviço, nunca `localhost` |
| alvo vermelho no Prometheus | `http://localhost:9090/targets` mostra o erro do scrape |
| dashboard vazio | gere tráfego: `for i in $(seq 1 200); do curl -s localhost/projeto-korp >/dev/null; done` |
| playbook falha no Docker | no WSL2, confira `systemd=true` em `/etc/wsl.conf` e reinicie a distribuição |
