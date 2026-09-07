# ansible/ — provisionamento do Desafio Korp com um único comando

Playbook que instala o Docker, cria a rede `korp-net`, constrói a imagem `http-server-projeto-korp`,
sobe os containers com `docker compose` (app, nginx, prometheus, grafana), configura o proxy reverso do
NGINX, valida o monitoramento e faz uma requisição HTTP exibindo a resposta no console
(brief Parte 3)

## Pré-requisitos (no alvo Linux — WSL2 Ubuntu 22.04/24.04 ou VM Debian/Ubuntu)

```bash
sudo apt-get update && sudo apt-get install -y pipx
pipx install ansible-core ansible-lint yamllint      # ansible-core >= 2.15
ansible-galaxy collection install -r ansible/requirements.yml
git clone https://github.com/OWNER/http-server-projeto-korp.git && cd http-server-projeto-korp
```

> WSL2: habilite o systemd (`/etc/wsl.conf` → `[boot] systemd=true`, depois `wsl --shutdown`) para o
> serviço `docker` ser gerenciado pelo `service`. O host Windows **não** executa o playbook.

O SDK Python `docker` (exigido por `community.docker.docker_network`/`docker_image`/`docker_container_*`)
é instalado **pela role `docker`** no alvo; não é passo manual.

## O comando único

```bash
ansible-playbook -i ansible/inventory.ini ansible/playbook.yml
```

Saída esperada: `PLAY RECAP` sem `failed`, e a task **"Exibir a resposta do serviço no console"** com
`{"nome": "Projeto Korp", "horario": "<UTC>"}`. Se o seu usuário acabou de entrar no grupo
`docker`, faça logout/login (ou `newgrp docker`) e rode de novo.

## Demo em duas etapas

| | Perfil padrão — a entrega | Perfil estendido — stack estendido (bônus) |
|---|---|---|
| Comando | `ansible-playbook -i ansible/inventory.ini ansible/playbook.yml` | `ansible-playbook -i ansible/inventory.ini ansible/playbook.yml -e compose_profiles='["full"]'` |
| Containers | 4: `http-server-projeto-korp`, `nginx`, `prometheus`, `grafana` | os 4 + 10 do perfil `full` |
| `scrape_full.yml` | **fora** do diretório do Prometheus (fica como `scrape_full.yml.disabled`) | no lugar, carregado por `POST /-/reload` |
| `/targets` | 2 alvos, 100 % UP, **zero vermelho** | 14 alvos UP |
| Verificações extras | — | Tempo/Loki prontos, Pyroscope UP, `/stub_status` na 8081, `probe_success=1` |

O perfil padrão é o que o avaliador roda, **sem flag nenhuma** (intacto). O perfil estendido só acontece se
`compose_profiles` incluir `full`.

Para voltar do perfil estendido ao perfil padrão, derrube também os serviços do perfil antes de reexecutar — o
`docker compose up` sem o perfil não para o que já está de pé:

```bash
docker compose --profile full down
ansible-playbook -i ansible/inventory.ini ansible/playbook.yml
```

### O que o perfil `full` sobe hoje

`alertmanager` · `otel-collector` · `tempo` · `loki` · `alloy` · `pyroscope` · `cadvisor` ·
`node-exporter` · `blackbox-exporter` · `nginx-exporter` — nenhum publica porta no host (só `expose:`),
todos com `cpus:`/`mem_limit:`. Os 12 jobs de scrape correspondentes vivem em
`observability/prometheus/scrape_full.yml`, separado do `prometheus.yml` justamente para o perfil padrão não
mostrar alvo vermelho.

### O interruptor do `scrape_full.yml`

`prometheus.yml` carrega `scrape_config_files: ["scrape_*.yml"]`. A role `monitoring`:

- **sem** `full` → renomeia `scrape_full.yml` para `scrape_full.yml.disabled` (o sufixo não casa o
  glob) e faz `POST /-/reload`;
- **com** `full` → desfaz a renomeação e recarrega.

Renomear em vez de apagar preserva um arquivo versionado e torna a operação reversível e idempotente
nos dois sentidos. Um glob que não casa arquivo nenhum **não é erro** para o Prometheus (verificado com
`promtool check config` em `prom/prometheus:v3.5.0`).

> **Pré-requisito no `compose.yml`:** o serviço `prometheus` precisa montar o **diretório**
> (`- ./observability/prometheus:/etc/prometheus:ro`) e não apenas `prometheus.yml` + `rules/`. Com
> mount por arquivo, o `scrape_full.yml` existe no host e é invisível dentro do container. A role
> `monitoring` verifica isso e falha com essa mensagem quando o perfil `full` está ativo.

## Idempotência

Rode o mesmo comando outra vez: o `PLAY RECAP` deve mostrar `changed=0`. É o que `` verifica com
`GATE_ANSIBLE_RUN=1 bash scripts/gate.sh --gates `. Vale para os duas etapas: repetir o perfil padrão e
repetir o perfil estendido devem dar `changed=0`; alternar entre eles muda exatamente uma coisa (o
`scrape_full.yml`) e dispara um reload.

## Checagens antes de executar

```bash
yamllint ansible/
ansible-lint --profile production ansible/playbook.yml
ansible-playbook -i ansible/inventory.ini --syntax-check ansible/playbook.yml
ansible-playbook -i ansible/inventory.ini ansible/playbook.yml --check --diff   # ensaio
ansible-playbook -i ansible/inventory.ini ansible/playbook.yml --list-tasks     # o que o comando fará
```

## Tags (reexecução parcial)

`--tags docker|network|app|nginx|monitoring|validate` — ex.: `ansible-playbook -i ansible/inventory.ini ansible/playbook.yml --tags validate`.

## Variáveis úteis (`-e`)

| Variável | Default | Uso |
|---|---|---|
| `app_version` | `dev` | tag da imagem / `build_info{version}` |
| `compose_profiles` | `` | `['full']` = perfil estendido: alertmanager, otel-collector, tempo, loki, alloy, pyroscope, cadvisor, node-exporter, blackbox-exporter, nginx-exporter |
| `project_root` | `<raiz do repo>` | onde estão `compose.yml`, `Dockerfile`, `nginx/`, `observability/` |
| `docker_users` | usuário do sudo | quem entra no grupo `docker` |
| `monitoring_promtool_image` | `prom/prometheus:v3.5.0` | imagem usada para `promtool check/test` (nada é instalado no alvo) |
| `nginx_network` | `korp-net` | rede usada no `nginx -t`; sem ela o teste falha em "host not found in upstream" |

## Mapa role → requisito do brief

| Role | Requisitos |
|---|---|
| `docker` |,  |
| `network` |,  |
| `app` | /08,..14,,  |
| `nginx` |..20,  (+ `stub_status.conf` no perfil `full`,) |
| `monitoring` |..29,,  (bônus) |
| `validate` |,,, /16 (+ `probe_success` no perfil `full`) |

## O que cada role valida (além de configurar)

- `nginx`: `nginx -t` no arquivo gerado **e** no diretório `conf.d` inteiro (pega conflito entre o
  proxy reverso e o `stub_status.conf`), presença do mount de diretório, e — no perfil `full` — que
  `http://127.0.0.1:8081/stub_status` responde de dentro do container.
- `monitoring`: `promtool check config`, `promtool check rules` em **todos** os `rules/*.rules.yml`
  encontrados (lista vinda de `find`, não fixa) e `promtool test rules` em todos os
  `rules/tests/*.test.yml`; containers do perfil ativo em execução; Prometheus e Grafana prontos;
  target do serviço UP; no perfil `full`, Tempo e Loki `healthy` (healthcheck = `/ready`) e
  Tempo/Loki/Pyroscope UP no Prometheus.
- `validate`: `uri` + `assert` + `debug` do JSON e, no perfil `full`, `probe_success=1` na
  sonda `korp_contract` do blackbox — a única verificação contínua que atravessa o NGINX.

## Troubleshooting

- `permission denied /var/run/docker.sock` → relogin após entrar no grupo `docker`.
- `docker_compose_v2` ausente → `ansible-galaxy collection install -r ansible/requirements.yml` (community.docker ≥ 3.10).
- `host not found in upstream` no `nginx -t` → a rede `korp-net` não existe ou o container do app está
  parado; rode `--tags network,app` antes de `--tags nginx`.
- validação falha com 502 → `docker compose logs nginx http-server-projeto-korp`; `docker network inspect korp-net`.
- Prometheus target `down` → `docker compose exec prometheus wget -qO- http://http-server-projeto-korp:8080/metrics`.
- `probe_success != 1` no perfil estendido → o caminho do usuário está quebrado de verdade; comece por
  `curl -v http://localhost/projeto-korp` e `docker compose logs blackbox-exporter`.
- Alvos vermelhos no `/targets` do perfil padrão → o `scrape_full.yml` ficou no diretório do Prometheus; a role
  `monitoring` o desativa, então verifique se a role rodou (`--tags monitoring`).
