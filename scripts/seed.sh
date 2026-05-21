#!/usr/bin/env bash
# Seed a local Aloqa backend with deterministic test users, a shared workspace,
# and a few pre-populated DM conversations for manual QA / frontend testing.
#
# Idempotent: re-running skips users / workspace / memberships / messages that
# already exist. Safe to run repeatedly against the same database.
#
# Usage:
#   ./scripts/seed.sh                     # against http://localhost:8090
#   ALOQA_API=http://host:8090/api/v1 ./scripts/seed.sh
#
# Requires: bash 3.2+, curl, jq.

set -euo pipefail

API="${ALOQA_API:-http://localhost:8090/api/v1}"
WS_SLUG="${ALOQA_WS_SLUG:-aloqa-test}"
WS_NAME="${ALOQA_WS_NAME:-Aloqa Test}"

for bin in curl jq; do
  command -v "$bin" >/dev/null || { echo "missing dependency: $bin" >&2; exit 1; }
done

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

jbody() { jq -nc "$@"; }

# log <msg...>
log() { printf '\033[36m==\033[0m %s\n' "$*"; }
skip() { printf '   \033[2mskip\033[0m %s\n' "$*"; }
note() { printf '   %s\n' "$*"; }

# http POST/GET helper that writes body to $TMP/resp and returns status code.
# usage: http_status METHOD URL [TOKEN] [JSON_BODY]
http_status() {
  local method=$1 url=$2 token=${3:-} body=${4:-}
  local -a args=(-sS -o "$TMP/resp" -w '%{http_code}' -X "$method" "$url")
  [[ -n "$token" ]] && args+=(-H "Authorization: Bearer $token")
  if [[ -n "$body" ]]; then
    args+=(-H 'Content-Type: application/json' -d "$body")
  fi
  curl "${args[@]}"
}

# Try logging in. Prints access token on success, empty string otherwise.
try_login() {
  local email=$1 pass=$2
  local code
  code=$(http_status POST "$API/auth/login" '' "$(jbody --arg e "$email" --arg p "$pass" '{email:$e,password:$p}')")
  if [[ "$code" == "200" ]]; then
    jq -r '.access_token' < "$TMP/resp"
  fi
}

# ensure_user EMAIL PASSWORD DISPLAY_NAME
# Outputs: "<user_id>\t<access_token>" on a single line, TAB-separated.
ensure_user() {
  local email=$1 pass=$2 name=$3
  local token id
  token=$(try_login "$email" "$pass")
  if [[ -n "$token" ]]; then
    skip "user $email exists" >&2
  else
    local code
    code=$(http_status POST "$API/auth/register" '' \
      "$(jbody --arg e "$email" --arg p "$pass" --arg n "$name" \
        '{email:$e,password:$p,display_name:$n}')")
    if [[ "$code" != "201" && "$code" != "200" ]]; then
      echo "register $email failed ($code): $(cat "$TMP/resp")" >&2
      exit 1
    fi
    log "registered $email" >&2
    token=$(try_login "$email" "$pass")
    [[ -n "$token" ]] || { echo "login after register failed for $email" >&2; exit 1; }
  fi
  id=$(curl -fsS "$API/users/me" -H "Authorization: Bearer $token" | jq -r .id)
  printf '%s\t%s\n' "$id" "$token"
}

# ensure_profile TOKEN POSITION DEPARTMENT
# Idempotent: PATCH /users/me always overwrites, so re-running keeps values
# in sync with this seed script.
ensure_profile() {
  local token=$1 position=$2 department=$3
  local code
  code=$(http_status PATCH "$API/users/me" "$token" \
    "$(jbody --arg p "$position" --arg d "$department" '{position:$p,department:$d}')")
  case "$code" in
    200|204) ;;
    *) echo "set profile failed ($code): $(cat "$TMP/resp")" >&2; exit 1 ;;
  esac
}

# ensure_workspace TOKEN NAME SLUG -> workspace id
ensure_workspace() {
  local token=$1 name=$2 slug=$3
  local id
  id=$(curl -fsS "$API/workspaces" -H "Authorization: Bearer $token" \
    | jq -r --arg s "$slug" 'map(select(.slug==$s)) | .[0].id // ""')
  if [[ -n "$id" ]]; then
    skip "workspace '$slug' exists" >&2
  else
    id=$(curl -fsS -X POST "$API/workspaces" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' \
      -d "$(jbody --arg n "$name" --arg s "$slug" '{name:$n,slug:$s,avatar_url:""}')" \
      | jq -r .id)
    log "created workspace '$slug' ($id)" >&2
  fi
  printf '%s' "$id"
}

# ensure_member TOKEN WS_ID EMAIL
ensure_member() {
  local token=$1 ws=$2 email=$3
  local code
  code=$(http_status POST "$API/workspaces/$ws/admin/members" "$token" \
    "$(jbody --arg e "$email" '{email:$e,role:"member"}')")
  case "$code" in
    200|201) log "invited $email" ;;
    409)     skip "$email is already a member" ;;
    *)       echo "invite $email failed ($code): $(cat "$TMP/resp")" >&2; exit 1 ;;
  esac
}

# ensure_dm TOKEN WS_ID TARGET_USER_ID -> channel id
ensure_dm() {
  local token=$1 ws=$2 target=$3
  # GetOrCreateDM on the backend is idempotent already.
  curl -fsS -X POST "$API/workspaces/$ws/channels/dm" \
    -H "Authorization: Bearer $token" \
    -H 'Content-Type: application/json' \
    -d "$(jbody --arg u "$target" '{user_id:$u}')" | jq -r .id
}

# ensure_channel CREATOR_TOKEN WS_ID NAME TOPIC TYPE -> channel id
# Looks up an existing channel by name within the workspace before creating.
ensure_channel() {
  local token=$1 ws=$2 name=$3 topic=$4 ctype=$5
  local id
  id=$(curl -fsS "$API/workspaces/$ws/channels" -H "Authorization: Bearer $token" \
    | jq -r --arg n "$name" 'map(select(.name==$n)) | .[0].id // ""')
  if [[ -n "$id" ]]; then
    skip "channel #$name exists" >&2
  else
    id=$(curl -fsS -X POST "$API/workspaces/$ws/channels" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' \
      -d "$(jbody --arg n "$name" --arg t "$topic" --arg k "$ctype" \
            '{name:$n,topic:$t,type:$k}')" | jq -r .id)
    log "created #$name ($id)" >&2
  fi
  printf '%s' "$id"
}

# ensure_channel_member USER_TOKEN WS_ID CHAN_ID
# Joins a public channel as the calling user; backend rejects already-member
# with 409 which we treat as a no-op.
ensure_channel_member() {
  local token=$1 ws=$2 chan=$3
  local code
  code=$(http_status POST "$API/workspaces/$ws/channels/$chan/join" "$token" '')
  case "$code" in
    200|204|201) ;;
    409) ;;  # already a member
    *) echo "join $chan failed ($code): $(cat "$TMP/resp")" >&2; exit 1 ;;
  esac
}

# channel_has_messages TOKEN WS_ID CHAN_ID -> exit 0 if yes
channel_has_messages() {
  local token=$1 ws=$2 chan=$3
  local count
  count=$(curl -fsS "$API/workspaces/$ws/channels/$chan/messages?limit=1" \
    -H "Authorization: Bearer $token" | jq '.items | length')
  [[ "$count" -gt 0 ]]
}

# send_msg TOKEN WS_ID CHAN_ID CONTENT
send_msg() {
  local token=$1 ws=$2 chan=$3 content=$4
  curl -fsS -X POST "$API/workspaces/$ws/channels/$chan/messages" \
    -H "Authorization: Bearer $token" \
    -H 'Content-Type: application/json' \
    -d "$(jbody --arg c "$content" '{content:$c}')" >/dev/null
}

# seed_thread CHAN_ID  LINE...
# Each LINE is "TOKEN_VAR_NAME|CONTENT". Sends sequentially, only if channel empty.
seed_thread() {
  local ws=$1 chan=$2 inviter_token=$3; shift 3
  if channel_has_messages "$inviter_token" "$ws" "$chan"; then
    skip "channel $chan already has messages"
    return 0
  fi
  log "seeding ${#@} messages into $chan"
  local entry token content
  for entry in "$@"; do
    token=${entry%%|*}
    content=${entry#*|}
    send_msg "$token" "$ws" "$chan" "$content"
    sleep 0.05
  done
}

# ----------------------------------------------------------------------------

log 'ensuring users'
IFS=$'\t' read -r ALICE_ID ALICE_TOKEN < <(ensure_user 'alice@aloqa.test' 'AlicePass123!' 'Alice Sharipova')
IFS=$'\t' read -r BOB_ID   BOB_TOKEN   < <(ensure_user 'bob@aloqa.test'   'BobPass123!'   'Bob Karimov')
IFS=$'\t' read -r CAROL_ID CAROL_TOKEN < <(ensure_user 'carol@aloqa.test' 'CarolPass123!' 'Carol Ismailova')
IFS=$'\t' read -r DAVID_ID DAVID_TOKEN < <(ensure_user 'david@aloqa.test' 'DavidPass123!' 'David Yusupov')
IFS=$'\t' read -r EVE_ID   EVE_TOKEN   < <(ensure_user 'eve@aloqa.test'   'EvePass123!'   'Eve Tursunova')
IFS=$'\t' read -r FRANK_ID FRANK_TOKEN < <(ensure_user 'frank@aloqa.test' 'FrankPass123!' 'Frank Abdullayev')

log 'ensuring user profiles (position + department)'
# Spread realistic long job titles across multiple departments so the FE
# member directory has something interesting to render. PATCH /users/me is
# idempotent and overwrites on every run.
ensure_profile "$ALICE_TOKEN" 'Director of Product, Collaboration Tools'              'Product'
ensure_profile "$BOB_TOKEN"   'Senior Software Engineer, Platform Infrastructure'    'Engineering'
ensure_profile "$CAROL_TOKEN" 'Principal Product Designer, Communication'            'Design'
ensure_profile "$DAVID_TOKEN" 'Engineering Manager, Realtime'                        'Engineering'
ensure_profile "$EVE_TOKEN"   'Head of People & Culture'                             'People & Culture'
ensure_profile "$FRANK_TOKEN" 'Finance Business Partner, R&D'                        'Finance'

log "ensuring workspace '$WS_SLUG'"
WS_ID=$(ensure_workspace "$ALICE_TOKEN" "$WS_NAME" "$WS_SLUG")

log 'ensuring memberships'
for email in bob@aloqa.test carol@aloqa.test david@aloqa.test eve@aloqa.test frank@aloqa.test; do
  ensure_member "$ALICE_TOKEN" "$WS_ID" "$email"
done

log 'ensuring public channels'
GENERAL_ID=$(ensure_channel "$ALICE_TOKEN" "$WS_ID" 'general' 'Company-wide announcements' 'public')
RANDOM_ID=$( ensure_channel "$ALICE_TOKEN" "$WS_ID" 'random'  'Off-topic and watercooler' 'public')
DEV_ID=$(    ensure_channel "$ALICE_TOKEN" "$WS_ID" 'dev'     'Engineering team chat'     'public')

log 'joining channels'
for token in "$BOB_TOKEN" "$CAROL_TOKEN" "$DAVID_TOKEN" "$EVE_TOKEN" "$FRANK_TOKEN"; do
  for chan in "$GENERAL_ID" "$RANDOM_ID" "$DEV_ID"; do
    ensure_channel_member "$token" "$WS_ID" "$chan"
  done
done

log 'seeding channel messages'

seed_thread "$WS_ID" "$GENERAL_ID" "$ALICE_TOKEN" \
  "$ALICE_TOKEN|Всем привет! Это наш общий канал — здесь анонсы и новости команды." \
  "$BOB_TOKEN|👋"  \
  "$CAROL_TOKEN|Привет, рада быть здесь!" \
  "$ALICE_TOKEN|Завтра в 10:00 — всекомандный синк, ссылку скину утром." \
  "$DAVID_TOKEN|Принято." \
  "$EVE_TOKEN|Я подключусь из Самарканда, связь может прыгать." \
  "$FRANK_TOKEN|Ок 👍" \
  "$BOB_TOKEN|Кстати, дизайн нового логина уехал на ревью — спасибо @carol!" \
  "$CAROL_TOKEN|🙏" \
  "$ALICE_TOKEN|Напоминаю: ретро спринта в пятницу в 16:00. Заполните доску до четверга, пожалуйста." \
  "$DAVID_TOKEN|Я заполнил, накидал и плюсы, и минусы." \
  "$EVE_TOKEN|Сегодня выкатили обновление поиска на стейдж. Скоро отдадим QA." \
  "$BOB_TOKEN|@eve спасибо! Уже глянул — релевантность стала заметно лучше." \
  "$ALICE_TOKEN|На следующей неделе подключаем нового PM, Тимура. Будет помогать с релиз-планом." \
  "$FRANK_TOKEN|Welcome on board заранее 🙂" \
  "$ALICE_TOKEN|Все слышали про Q3-цели? Я скину сегодня документ на ревью." \
  "$CAROL_TOKEN|Жду, чтобы заложить дизайн-итерации в график." \
  "$DAVID_TOKEN|Тоже жду — хочу понять, сколько инфраструктурного бэклога влезет." \
  "$ALICE_TOKEN|Документ: https://notion.so/aloqa/q3-goals-2026 — пожалуйста, до пятницы оставьте комменты." \
  "$EVE_TOKEN|Прочитала, оставила пару заметок по поиску." \
  "$BOB_TOKEN|У меня вопрос по фокусу на мобильном — поднимем на синке." \
  "$ALICE_TOKEN|Окей, добавлю в повестку." \
  "$DAVID_TOKEN|Кстати, прод-инцидент с очередями вчера — постмортем готов, шлю в #dev." \
  "$ALICE_TOKEN|Спасибо. Закрепим в #dev и приложим к ретро." \
  "$CAROL_TOKEN|Команда, если кому нужна обновлённая иконка приложения — лежит в Figma → /icons/v3." \
  "$FRANK_TOKEN|Скачал, спасибо." \
  "$EVE_TOKEN|Завтра удалёнка? Дома мастер по интернету." \
  "$ALICE_TOKEN|Конечно, без вопросов." \
  "$BOB_TOKEN|Кто едет на конференцию в Ташкенте 18-го? Хочу скоординироваться по дороге." \
  "$DAVID_TOKEN|Я. Можем взять такси на двоих." \
  "$ALICE_TOKEN|Я тоже еду, но днём раньше — лекция от партнёров." \
  "$CAROL_TOKEN|Я выступаю с докладом про дизайн-токены 🥲 нервничаю." \
  "$EVE_TOKEN|У тебя получится! Ты уже репетировала на прошлой неделе — всё было четко." \
  "$BOB_TOKEN|+1, будем болеть в зале." \
  "$ALICE_TOKEN|Маленькая бытовуха: с понедельника обед в офисе с 13:00 до 14:00 (раньше было 12:30). Кухонная команда передаёт извинения." \
  "$FRANK_TOKEN|Окей, перенесу синк с дизайнерами." \
  "$ALICE_TOKEN|И последнее: HR попросил всех заполнить annual review form до 25 числа. Ссылка в почте." \
  "$DAVID_TOKEN|Готово." \
  "$CAROL_TOKEN|Заполню сегодня вечером."

seed_thread "$WS_ID" "$RANDOM_ID" "$BOB_TOKEN" \
  "$BOB_TOKEN|Кто пьёт кофе по утрам? Поделитесь рецептом идеальной заварки ☕️" \
  "$DAVID_TOKEN|V60, помол средний, вода 92°. Всё остальное — ересь." \
  "$EVE_TOKEN|Холодный брю forever 🧊" \
  "$ALICE_TOKEN|Эспрессо + овсянка. Минимализм." \
  "$FRANK_TOKEN|Я просто завариваю растворимый и не страдаю 🤷‍♂️" \
  "$CAROL_TOKEN|Frank, нет 😭" \
  "$BOB_TOKEN|Frank, после тебя дегустация не нужна 😄" \
  "$FRANK_TOKEN|Народ, я слабый человек 😅" \
  "$EVE_TOKEN|Кстати, в Самарканде открыли новое место — Coffee Lab. Если кто будет — рекомендую." \
  "$CAROL_TOKEN|У нас в Ташкенте есть Bro Coffee — тоже годно." \
  "$ALICE_TOKEN|Тема скоро уйдёт в офтоп окончательно 😄" \
  "$BOB_TOKEN|Кто что слушает во время кодинга? Накидайте плейлистов." \
  "$DAVID_TOKEN|Эмбиент и lo-fi. Голоса отвлекают." \
  "$EVE_TOKEN|Я под Hans Zimmer кодю — кажусь себе важной 😎" \
  "$CAROL_TOKEN|Phoebe Bridgers, но только когда не в дедлайне." \
  "$FRANK_TOKEN|Я слушаю белый шум, остальное не заходит во время работы." \
  "$ALICE_TOKEN|Кто куда планирует на майские? У меня — горы." \
  "$BOB_TOKEN|Дача с семьёй. Простой план, но идеальный." \
  "$DAVID_TOKEN|Сижу в городе. Хочу наконец пройти Witcher 3 до конца 🐺" \
  "$EVE_TOKEN|@david ты её уже три раза проходил 😄" \
  "$DAVID_TOKEN|И это не предел." \
  "$CAROL_TOKEN|Я на конференцию по дизайну в Дубае. Программа жирная." \
  "$FRANK_TOKEN|Завидую. Я пока ещё разгребаю переезд." \
  "$BOB_TOKEN|Frank, как тебе квартира?" \
  "$FRANK_TOKEN|Огонь, но интернет придётся менять — пинг невероятный." \
  "$ALICE_TOKEN|Кстати, кто-нибудь читал \"Designing Data-Intensive Applications\"? Хочу впечатления." \
  "$DAVID_TOKEN|Читал. Главы про репликацию и консенсус — золото." \
  "$EVE_TOKEN|Слушала её как аудиокнигу. Местами сложно без диаграмм." \
  "$BOB_TOKEN|Я взял её на майские — попробую дочитать наконец."

seed_thread "$WS_ID" "$DEV_ID" "$ALICE_TOKEN" \
  "$ALICE_TOKEN|Завели канал для инженерных обсуждений — PR'ы, инциденты, архитектурные холивары." \
  "$DAVID_TOKEN|👍 закрепить бы FAQ по локальному setup'у." \
  "$ALICE_TOKEN|+1, добавлю в pinned после онбординга." \
  "$BOB_TOKEN|Сегодня нашёл утечку в WS-клиенте при reconnect — фикс в PR #142." \
  "$CAROL_TOKEN|Гляну в обед." \
  "$DAVID_TOKEN|Спасибо. Я пока разбираюсь с гонкой в SearchWorker." \
  "$EVE_TOKEN|У кого-то падает миграция 026 на свежей БД? Или только у меня?" \
  "$ALICE_TOKEN|Eve, проверь что переменная DB_VALIDATE_MIGRATIONS_ON_START=true." \
  "$EVE_TOKEN|Ага, помогло, спасибо!" \
  "$FRANK_TOKEN|Я подключился к проекту, какой стек у фронта?" \
  "$BOB_TOKEN|Next.js 16 (App Router), TanStack Query, Zustand, RN на десктоп через Tauri." \
  "$FRANK_TOKEN|Понял, погружаюсь." \
  "$DAVID_TOKEN|Закинул постмортем вчерашнего инцидента: https://notion.so/aloqa/pm-2026-05-13" \
  "$ALICE_TOKEN|Спасибо. Главные action items: 1) алерт на длину очереди, 2) circuit breaker на воркере, 3) дашборд по retry-rate." \
  "$BOB_TOKEN|По 1 и 3 могу взять — давно хочу пощупать Grafana alerting properly." \
  "$DAVID_TOKEN|Окей, breaker за мной. На неделе тикет заведу." \
  "$EVE_TOKEN|У кого-нибудь была проблема с pgvector расширением после апдейта на Postgres 16? У меня индексы не пересчитываются." \
  "$DAVID_TOKEN|@eve у меня то же самое было. Решилось REINDEX CONCURRENTLY + пересоздание extension. Подробности в треде вчера в DM." \
  "$EVE_TOKEN|Гляну, спасибо." \
  "$BOB_TOKEN|Команда, по WebSocket transport — переходим на binary frames для голоса, текстовые остаются JSON. Кто-то против?" \
  "$DAVID_TOKEN|Логично. Latency должен упасть." \
  "$ALICE_TOKEN|Согласна. Только давайте feature flag, чтобы можно было быстро откатить." \
  "$BOB_TOKEN|Конечно, без флага в прод не пушим." \
  "$CAROL_TOKEN|Дизайн-сайд: у нас раскатан Storybook v9, можно смотреть компоненты по новому URL." \
  "$FRANK_TOKEN|Открыл — топ. Жаль только тёмная тема ещё не покрыта." \
  "$CAROL_TOKEN|Frank, до конца недели накачу токены — будет полное покрытие." \
  "$DAVID_TOKEN|Ребят, кто-то баловался с pprof в нашем сервере? Где лежат endpoints?" \
  "$ALICE_TOKEN|/debug/pprof/* доступны только в dev окружении. В prod — за VPN, ручка на 6060." \
  "$DAVID_TOKEN|Принял." \
  "$EVE_TOKEN|Поделюсь находкой: pq_stat_statements показывает, что 70% времени у поиска уходит в один JOIN. Сегодня заведу тикет на оптимизацию." \
  "$BOB_TOKEN|Огонь, перформанс улучшения будут заметны." \
  "$ALICE_TOKEN|Команда, у нас холивар в чате PM-ов про monorepo vs polyrepo. На нас ругаются за tooling. Кто хочет высказать аргументы — добавлю в slide-deck." \
  "$DAVID_TOKEN|Я готов выйти и защищать монорепу 😄 у меня уже презентация была." \
  "$BOB_TOKEN|@david поделись слайдами заранее, попредполагаю возражения." \
  "$FRANK_TOKEN|Я свежим глазом могу проверить — на прошлом месте у нас был polyrepo, в курсе плюсов и минусов." \
  "$ALICE_TOKEN|Топ. В пятницу созвон на 40 минут — поделюсь календарём." \
  "$CAROL_TOKEN|+1, тоже хочу присутствовать." \
  "$EVE_TOKEN|И вопрос: на тестах появилась флейка в чате — race в setLastReadAt. У кого-нибудь воспроизводится?" \
  "$DAVID_TOKEN|Eve, видел один раз. Подозреваю клок в моках. Можем в среду посидеть вдвоём — потанцуем над ним." \
  "$EVE_TOKEN|Договорились."

log 'ensuring DM threads'

DM=$(ensure_dm "$ALICE_TOKEN" "$WS_ID" "$CAROL_ID")
seed_thread "$WS_ID" "$DM" "$ALICE_TOKEN" \
  "$ALICE_TOKEN|Carol, привет! Можешь посмотреть ревью на PR #128 сегодня?" \
  "$CAROL_TOKEN|Привет, Alice! Да, в течение часа гляну." \
  "$ALICE_TOKEN|Супер, спасибо. Там в основном миграция БД и пара тестов." \
  "$CAROL_TOKEN|Ок, отпишу когда закончу." \
  "$ALICE_TOKEN|👍" \
  "$CAROL_TOKEN|Глянула. По миграции вопросов нет. По тестам — есть пара мест, где можно вынести setup в helper, я оставила комменты." \
  "$ALICE_TOKEN|Спасибо! Сейчас гляну." \
  "$CAROL_TOKEN|Ещё: дизайн логина — добавила второй state для slow network. Скинула в Figma." \
  "$ALICE_TOKEN|Видела, выглядит круто. Bob уже верстает?" \
  "$CAROL_TOKEN|Да, в обед скинет первую итерацию." \
  "$ALICE_TOKEN|Кстати, по поводу следующего спринта — что у тебя в работе?" \
  "$CAROL_TOKEN|Дизайн настроек профиля и редизайн уведомлений. Плюс хочу подсобрать research по онбордингу." \
  "$ALICE_TOKEN|Топ. Research я бы вынесла отдельно — обсудим в понедельник на 1:1." \
  "$CAROL_TOKEN|Окей, занесу в повестку." \
  "$ALICE_TOKEN|Carol, забыла: можно тебя попросить нарисовать иконку для нового тарифа?" \
  "$CAROL_TOKEN|Конечно, дай контекст — какой вайб у тарифа?" \
  "$ALICE_TOKEN|Premium, для команд от 50 человек. Подсвечиваем enterprise-ность." \
  "$CAROL_TOKEN|Поняла. Сделаю варианта три, выберем." \
  "$ALICE_TOKEN|Спасибо 🙏" \
  "$CAROL_TOKEN|Ещё момент — у нас спорят дизайнеры из соседней команды о шрифте. Можешь подключиться к решению?" \
  "$ALICE_TOKEN|Могу. Пятничный синк подойдёт?" \
  "$CAROL_TOKEN|Да, идеально."

DM=$(ensure_dm "$ALICE_TOKEN" "$WS_ID" "$DAVID_ID")
seed_thread "$WS_ID" "$DM" "$ALICE_TOKEN" \
  "$ALICE_TOKEN|David, добрый день. Деплой стейджа в обед прошёл?" \
  "$DAVID_TOKEN|Да, всё штатно. P95 латенси в норме, ошибок нет." \
  "$ALICE_TOKEN|Замечательно. На прод выкатываем в пятницу?" \
  "$DAVID_TOKEN|Да, если на стейдже до четверга не всплывёт сюрпризов." \
  "$ALICE_TOKEN|Как тестовый прогон нагрузки? Хочу понимать запас по headroom." \
  "$DAVID_TOKEN|Тестировали 3х от средней. Сервер держит, БД на пике уходила в 80% CPU — есть запас, но узкое место уже видно." \
  "$ALICE_TOKEN|По итогам прогона — мы можем выложить план оптимизации? Хочу обсудить с PM-ами." \
  "$DAVID_TOKEN|Сегодня к концу дня скину." \
  "$ALICE_TOKEN|🙏" \
  "$DAVID_TOKEN|И ещё — мы выкатываем feature flag фреймворк. Хочу прогнать его через тебя — не повторим ли архитектурные ошибки прошлого года." \
  "$ALICE_TOKEN|Конечно. Заодно посмотрим, как он интегрируется с нашим A/B-стеком." \
  "$DAVID_TOKEN|Заведу doc-черновик к среде." \
  "$ALICE_TOKEN|Окей. И, David, я тебе хотела сказать спасибо за работу в инциденте на прошлой неделе — ты вовремя поднял breaker." \
  "$DAVID_TOKEN|Спасибо. Хотя если честно, чисто реактивно. Хочу превентивно научиться видеть такие штуки." \
  "$ALICE_TOKEN|Это уже следующий уровень — давай вместе подумаем над сигнальной системой. Можно на неделе." \
  "$DAVID_TOKEN|Согласен. Запишу на пятницу." \
  "$ALICE_TOKEN|Топ. И ещё момент — у тебя как с отпуском? HR жалуется, что нет в системе." \
  "$DAVID_TOKEN|Сегодня внесу. Планирую первую неделю июня." \
  "$ALICE_TOKEN|Принято."

DM=$(ensure_dm "$BOB_TOKEN" "$WS_ID" "$CAROL_ID")
seed_thread "$WS_ID" "$DM" "$BOB_TOKEN" \
  "$BOB_TOKEN|Carol, доброе утро. Скинь, пожалуйста, ссылку на финальный дизайн логина." \
  "$CAROL_TOKEN|Доброе! Сейчас, секунду..." \
  "$CAROL_TOKEN|https://figma.com/file/aloqa-auth-v3" \
  "$BOB_TOKEN|Поймал, спасибо. Сегодня сверстаю и кину тебе в ревью." \
  "$CAROL_TOKEN|Жду 🙂" \
  "$BOB_TOKEN|Carol, по дизайну — у тебя где-то заложен loading state для медленной сети?" \
  "$CAROL_TOKEN|Да, во втором фрейме сверху. Skeleton + сообщение через 2 сек ожидания." \
  "$BOB_TOKEN|Поняла, делаю с тайм-аутом. По кнопке — оставить размер 48 или 44?" \
  "$CAROL_TOKEN|48 на мобиле, 44 на desktop. Так в гайде." \
  "$BOB_TOKEN|Окей. И насчёт текста ошибки — \"неверный пароль\" звучит резко, как считаешь?" \
  "$CAROL_TOKEN|Согласна. Давай заменим на \"Не удалось войти. Проверьте email и пароль\"." \
  "$BOB_TOKEN|Топ. Меняю." \
  "$CAROL_TOKEN|Bob, кину тебе ещё один фрейм — состояние \"учётка заблокирована\". Это редкий кейс, но надо чтобы он не выглядел страшно." \
  "$BOB_TOKEN|Жду. Я как раз думал, что у нас провал в этом состоянии." \
  "$CAROL_TOKEN|Готово, frame 12. Там же текст для саппорт-чата." \
  "$BOB_TOKEN|Идеально, отрисую." \
  "$CAROL_TOKEN|Кстати, ты уже подключил иконки от Phosphor?" \
  "$BOB_TOKEN|Да, через @phosphor-icons/react. Они весят меньше, чем lucide." \
  "$CAROL_TOKEN|Отлично. Тогда можешь использовать прямо названия из Figma." \
  "$BOB_TOKEN|Лучшая интеграция дизайн ↔ код 🙌" \
  "$CAROL_TOKEN|Не зря неделю траила её 🙂"

DM=$(ensure_dm "$BOB_TOKEN" "$WS_ID" "$DAVID_ID")
seed_thread "$WS_ID" "$DM" "$BOB_TOKEN" \
  "$BOB_TOKEN|David, по ALOQA-77 как прогресс?" \
  "$DAVID_TOKEN|Завтра доделаю, остался один баг с воркером очередей." \
  "$BOB_TOKEN|Окей, держи в курсе если упрёшься." \
  "$DAVID_TOKEN|Принял."

DM=$(ensure_dm "$ALICE_TOKEN" "$WS_ID" "$BOB_ID")
seed_thread "$WS_ID" "$DM" "$ALICE_TOKEN" \
  "$ALICE_TOKEN|Bob, есть пара минут? Хочу синкнуться по релизу." \
  "$BOB_TOKEN|Да, конечно. Сейчас вернусь с кофе." \
  "$ALICE_TOKEN|Ок." \
  "$BOB_TOKEN|Вернулся. Что по релизу?" \
  "$ALICE_TOKEN|Хочу зафиксировать scope — три фичи (поиск, календарь, звонки) и багфикс с миграциями." \
  "$BOB_TOKEN|Согласен. Остальное в следующий спринт." \
  "$ALICE_TOKEN|По звонкам — у нас же ещё recording в работе? Войдёт?" \
  "$BOB_TOKEN|Recording в work-in-progress, в этот релиз не пушу. Будет в 0.5." \
  "$ALICE_TOKEN|Окей. Тогда в release notes отметим как coming soon." \
  "$BOB_TOKEN|Логично. Ты дашь черновик notes — я подкорректирую техническую часть." \
  "$ALICE_TOKEN|Договорились. Завтра до обеда скину." \
  "$BOB_TOKEN|Ещё момент — заметил, что у нас флейка в тестах WS клиента. Хочешь, чтобы я заблокировал релиз пока не починим?" \
  "$ALICE_TOKEN|Насколько часто падает?" \
  "$BOB_TOKEN|Один раз из ~30 прогонов. Race condition в моке таймера." \
  "$ALICE_TOKEN|Не блокирующее. Заведи тикет и пушаем в релиз — потом починим." \
  "$BOB_TOKEN|Окей, ALOQA-141 завёл." \
  "$ALICE_TOKEN|Спасибо. И ещё — на demo-day кто из вас презентует звонки?" \
  "$BOB_TOKEN|Я. Уже репетировал, готов." \
  "$ALICE_TOKEN|Топ. Не забудь показать качество видео в плохой сети — это сильный месседж." \
  "$BOB_TOKEN|Учту. У нас как раз готов сценарий с 30% потерь пакетов." \
  "$ALICE_TOKEN|Идеально. Спасибо за подготовку 🙏" \
  "$BOB_TOKEN|Команда тащит, я просто фронтмен 😄"

DM=$(ensure_dm "$ALICE_TOKEN" "$WS_ID" "$EVE_ID")
seed_thread "$WS_ID" "$DM" "$ALICE_TOKEN" \
  "$ALICE_TOKEN|Eve, привет. Как тебе первая неделя?" \
  "$EVE_TOKEN|Привет, Alice! Норм, втягиваюсь. Локальный сетап заработал со второй попытки." \
  "$ALICE_TOKEN|Отлично. Если что — спрашивай, не стесняйся." \
  "$EVE_TOKEN|Спасибо 🙏"

DM=$(ensure_dm "$ALICE_TOKEN" "$WS_ID" "$FRANK_ID")
seed_thread "$WS_ID" "$DM" "$ALICE_TOKEN" \
  "$ALICE_TOKEN|Frank, добро пожаловать! Если есть вопросы по проекту — пиши." \
  "$FRANK_TOKEN|Спасибо! Где почитать про деплой?" \
  "$ALICE_TOKEN|В docs/ есть observability-and-sre.md и performance-and-latency.md — это база." \
  "$FRANK_TOKEN|Принято, изучу."

DM=$(ensure_dm "$BOB_TOKEN" "$WS_ID" "$EVE_ID")
seed_thread "$WS_ID" "$DM" "$BOB_TOKEN" \
  "$BOB_TOKEN|Eve, видел твой PR — мелкий комментарий по типизации." \
  "$EVE_TOKEN|Окей, посмотрю через час." \
  "$BOB_TOKEN|Не срочно."

DM=$(ensure_dm "$BOB_TOKEN" "$WS_ID" "$FRANK_ID")
seed_thread "$WS_ID" "$DM" "$BOB_TOKEN" \
  "$BOB_TOKEN|Frank, есть Figma access? Скинь почту — добавлю в проект." \
  "$FRANK_TOKEN|f.abdullayev@aloqa.test" \
  "$BOB_TOKEN|Добавил."

DM=$(ensure_dm "$CAROL_TOKEN" "$WS_ID" "$DAVID_ID")
seed_thread "$WS_ID" "$DM" "$CAROL_TOKEN" \
  "$CAROL_TOKEN|David, расскажи как у вас на бэке решают rate limiting?" \
  "$DAVID_TOKEN|Token bucket в Redis, ключ — user+route. Лимиты в конфиге." \
  "$CAROL_TOKEN|Понял, спасибо." \
  "$DAVID_TOKEN|Если будут вопросы — пинг."

DM=$(ensure_dm "$CAROL_TOKEN" "$WS_ID" "$EVE_ID")
seed_thread "$WS_ID" "$DM" "$CAROL_TOKEN" \
  "$CAROL_TOKEN|Eve, привет! Дизайн-токены живут в packages/ui-kit-web/src/tokens.ts." \
  "$EVE_TOKEN|Спасибо! Гляну сегодня."

DM=$(ensure_dm "$CAROL_TOKEN" "$WS_ID" "$FRANK_ID")
seed_thread "$WS_ID" "$DM" "$CAROL_TOKEN" \
  "$CAROL_TOKEN|Frank, у нас в Figma есть готовая библиотека компонентов — пришлю инвайт." \
  "$FRANK_TOKEN|👍 жду."

DM=$(ensure_dm "$DAVID_TOKEN" "$WS_ID" "$EVE_ID")
seed_thread "$WS_ID" "$DM" "$DAVID_TOKEN" \
  "$DAVID_TOKEN|Eve, увидел падение в SearchWorker на твоей ветке." \
  "$EVE_TOKEN|Уже фикшу, забыла обработать nil контекст." \
  "$DAVID_TOKEN|Окей."

DM=$(ensure_dm "$DAVID_TOKEN" "$WS_ID" "$FRANK_ID")
seed_thread "$WS_ID" "$DM" "$DAVID_TOKEN" \
  "$DAVID_TOKEN|Frank, если будешь трогать API — у нас все ручки в /api/v1/*." \
  "$FRANK_TOKEN|Спасибо, посмотрю swagger."

DM=$(ensure_dm "$EVE_TOKEN" "$WS_ID" "$FRANK_ID")
seed_thread "$WS_ID" "$DM" "$EVE_TOKEN" \
  "$EVE_TOKEN|Frank, привет! Мы оба новички, давай пройдёмся по локальному сетапу вместе?" \
  "$FRANK_TOKEN|Привет! С удовольствием. Завтра 11:00 ок?" \
  "$EVE_TOKEN|Договорились."

echo
log 'done'
note "workspace_id=$WS_ID"
note "channels: #general, #random, #dev — all 6 users joined"
note 'DMs: all 15 pairs between the 6 users are populated'
note 'Credentials (all valid against the password policy):'
note '  alice@aloqa.test / AlicePass123!'
note '  bob@aloqa.test   / BobPass123!'
note '  carol@aloqa.test / CarolPass123!'
note '  david@aloqa.test / DavidPass123!'
note '  eve@aloqa.test   / EvePass123!'
note '  frank@aloqa.test / FrankPass123!'
