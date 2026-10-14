# gemini-web2api-go

**Gemini web → OpenAI-compatible API.** A self-hosted proxy that turns the free [gemini.google.com](https://gemini.google.com) web app into `/v1/chat/completions` — **no Google API key, no paid quota**. Single Go binary, real Chrome TLS fingerprint, cookie pool with automatic renewal, proxy pool, built-in admin dashboard, SQLite / MySQL / PostgreSQL. Image, music, canvas and video generation (`/v1/videos`) included.

<img src="docs/banner.svg" alt="gemini-web2api-go — Gemini web to OpenAI-compatible API proxy" width="100%">

[![Release](https://img.shields.io/github/v/release/zexadev/gemini-web2api-go?label=release)](https://github.com/zexadev/gemini-web2api-go/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/zexadev/gemini-web2api-go/total?label=downloads)](https://github.com/zexadev/gemini-web2api-go/releases)
[![Stars](https://img.shields.io/github/stars/zexadev/gemini-web2api-go?style=flat&label=stars)](https://github.com/zexadev/gemini-web2api-go/stargazers)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.21%2B-00ADD8.svg)](https://golang.org)
[![Docker](https://img.shields.io/badge/docker-distroless-blue)](Dockerfile)

Español | [English](README_EN.md)

Convierte la web de Google Gemini en una API compatible con OpenAI. **Binario único**, **cero cuentas** (funciona de forma anónima), **huella real de Chrome 146**, **persistencia SQLite**, con **panel de administración en chino**.

---

## Qué es esto

Convierte este tipo de llamada:
```
[OpenAI SDK / Cherry Studio / Cursor / dify / newapi / ...]
    ↓ http://localhost:8083/v1/chat/completions
[gemini-web2api-go]
    ↓ ingeniería inversa del protocolo web de gemini.google.com
[Frontend web de Google Gemini]
```

No es una envoltura sobre la API oficial de Google ([generativelanguage.googleapis.com](https://generativelanguage.googleapis.com)): hace **reverse proxy directo del protocolo del navegador**, por lo que **no necesita Google API Key ni cuota de pago**.

## Funcionalidades

**Endpoints**
- Compatible con OpenAI: `/v1/chat/completions`, `/v1/models`, `/v1/responses`
- `/v1/videos`: generación de vídeo asíncrona con forma OpenAI (Sora) — `POST` crea la tarea, `GET /v1/videos/{id}` sondea el estado, `GET /v1/videos/{id}/content` descarga el MP4 (requiere una cuenta Pro con sesión iniciada)
- Chat normal con streaming real: cada frame que emite el upstream se reenvía como delta. Las peticiones con `tools` se **bufferizan** a propósito (se reciben completas, se validan/corrigen y se reintenta si hace falta, y luego se emiten), porque el bloque `tool_call` necesita el texto completo para parsearse y una vez emitidos los bytes ya no se pueden retirar
- Autenticación con Bearer token / `x-api-key`; la key se puede rotar desde el panel
- `usage` se calcula con tiktoken; `reasoning_tokens` se cuenta aparte y no se incluye en `completion_tokens`

**Modelos**
- `gemini-3.6-flash` y `gemini-3.5-flash-lite` disponibles de forma anónima, con búsqueda en internet
- `gemini-3.1-pro` disponible tras asociar una cookie; cada respuesta incluye cadena de pensamiento (`reasoning_content`)
- Los tres modelos tienen versión `-thinking` (pensamiento extendido), disponible tras asociar una cookie
- La respuesta registra qué modelo usó **realmente** el servidor; una degradación silenciosa se detecta de un vistazo

**Sin bloqueos / funcionamiento duradero**
- utls imita la huella TLS real de Chrome 146, no el handshake por defecto del SDK
- Límite de tasa independiente por IP de salida: tres niveles de concurrencia / RPM / RPH
- Pool de proxies: alta/baja/modificación en caliente, disyuntor por fallos, programación round-robin; cada proxy es un slot de límite de tasa independiente
- Pool de cookies: varias cuentas de Google rotan dando prioridad a la menos usada recientemente, renovación automática + keep-alive; cada cuenta queda fijada a su propia salida

**Operación**
- Binario único, compilación cruzada para 6 plataformas; imagen de contenedor basada en distroless
- Persistencia SQLite: 30 días de detalle de peticiones + estadísticas agregadas permanentes (opcional MySQL / PostgreSQL, basta con definir `SQL_DSN`)
- Panel de administración en chino: resumen / registro de peticiones / pool de proxies / pool de cookies / ajustes; los cambios de configuración se aplican al instante
- **El prompt y el contenido de las respuestas nunca se almacenan en la base de datos**; solo se guardan metadatos (longitud, latencia, modelo, estado)

## Inicio rápido

### Descargar el binario (lo más sencillo)

Descarga la versión correspondiente a tu plataforma desde [Releases](https://github.com/zexadev/gemini-web2api-go/releases);
sin Go, sin Docker: un solo archivo lo es todo:

```bash
chmod +x gemini-web2api-go_*
./gemini-web2api-go_* --port 8083 --admin-token your-admin-token
```

Los datos se guardan por defecto en `./data/gemini.db`; para cambiar la ubicación añade `--db /your/path.db`.

Si quieres usar MySQL / PostgreSQL, basta con definir la variable de entorno `SQL_DSN` (si no se define, es SQLite; sin cambios):

```bash
# MySQL (también acepta el formato nativo de go-sql-driver user:pass@tcp(host:3306)/dbname)
SQL_DSN="mysql://user:pass@host:3306/dbname"
# PostgreSQL
SQL_DSN="postgres://user:pass@host:5432/dbname?sslmode=disable"
```

La creación de tablas es automática; las tres bases comparten el mismo esquema. Para un solo equipo, lo más sencillo es el SQLite por defecto; MySQL/PG solo hace falta cuando varias instancias comparten un mismo pool.

### Docker (sin código fuente)

```bash
docker run -d --name gemini-web2api \
  -p 127.0.0.1:8083:8083 \
  -v "$PWD/data:/data" \
  -e ADMIN_TOKEN=your-admin-token \
  ghcr.io/zexadev/gemini-web2api-go:latest
```

Si usas compose, basta con descargar `docker-compose.yml` por separado, sin necesidad de clonar:

```bash
curl -O https://raw.githubusercontent.com/zexadev/gemini-web2api-go/main/docker-compose.yml
ADMIN_TOKEN=your-admin-token docker compose up -d
```

### Ejecutar desde el código fuente

Si tienes Go instalado, no hace falta pasar por Docker:

```bash
git clone https://github.com/zexadev/gemini-web2api-go
cd gemini-web2api-go
go build -o gemini-web2api-go .
./gemini-web2api-go --port 8083 --admin-token your-admin-token
```

Si modificas el código y quieres ejecutarlo en un contenedor, descomenta las dos líneas de `build:` en `docker-compose.yml` y ejecuta `docker compose up -d --build`.

Al arrancar verás el banner:

```
gemini-web2api-go v4.0.0
  Listening:   http://0.0.0.0:8083
  Base URL:    http://localhost:8083/v1
  API key:     sk-gemini-XX...XXXX  (mutable in admin UI)
  Admin UI:    http://localhost:8083/admin  (token auth)
  DB:          ./data/gemini.db
  Models:      [gemini-3.5-flash-lite gemini-3.6-flash]
  Cookie:      none (anonymous)
  Proxy:       none
  Impersonate: chrome_146
  Tokenizer:   tiktoken cl100k_base
  Per-IP limit: concurrency=5 / RPM=30 / RPH=80
  Retry:       3x / 2s
```

En el primer arranque se genera automáticamente la API key (en el banner aparece censurada; el valor completo está en la página «Ajustes» del panel de administración).

### Llamada

```bash
curl http://localhost:8083/v1/chat/completions \
  -H "Authorization: Bearer sk-gemini-..." \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.6-flash",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

El SDK de OpenAI para Python también funciona directamente:

```python
from openai import OpenAI
client = OpenAI(
    base_url="http://localhost:8083/v1",
    api_key="sk-gemini-..."  # ver en el panel de admin
)
resp = client.chat.completions.create(
    model="gemini-3.6-flash",
    messages=[{"role": "user", "content": "explica el entrelazamiento cuántico"}]
)
print(resp.choices[0].message.content)
```

En Windows PowerShell, `curl` es un alias de `Invoke-WebRequest`, que reinterpreta las comillas del JSON; hay que usar
`curl.exe` con `--%`:

```powershell
curl.exe --% http://127.0.0.1:8083/v1/chat/completions -H "Content-Type: application/json" -H "Authorization: Bearer sk-gemini-..." -d "{\"model\":\"gemini-3.6-flash\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello!\"}]}"
```

## Conexión de clientes

Cualquier cliente compatible con OpenAI (Cherry Studio / ChatBox / Open WebUI / dify / Cursor / …) se configura igual:

| Campo | Valor |
|---|---|
| Base URL / dirección de la API | `http://localhost:8083/v1` (algunos clientes solo necesitan `http://localhost:8083` y añaden `/v1` por su cuenta) |
| API Key | la que aparece en la página «Ajustes» del panel de administración, `sk-gemini-…` |
| Modelo | `gemini-3.6-flash` |

**Crear un canal en newapi / one-api**: elige el tipo OpenAI, rellena Base URL con `http://localhost:8083` (si newapi está
en el mismo docker, escribe `http://host.docker.internal:8083` o el nombre del contenedor), pon la API key como clave y
`gemini-3.6-flash,gemini-3.5-flash-lite` como modelos.

**Codex CLI** usa `/v1/responses`; basta con apuntar la base url a `http://localhost:8083/v1` (ese endpoint está implementado,
pero no es streaming incremental; consulta la tabla de cobertura del protocolo más abajo).

**Gemini CLI no se puede conectar**: necesita el formato nativo de Google `/v1beta/models/{model}:generateContent`;
este proyecto solo expone interfaces con forma OpenAI, no implementa `/v1beta`.

Además hay un health check sin autenticación `GET /` que devuelve `{"status":"ok","version":…,"models":[…]}` para sondas de disponibilidad.

## MCP (herramienta web_search)

Además de la interfaz OpenAI, el mismo proceso y el mismo puerto exponen un **MCP server**, en `/mcp`, que
expone la **búsqueda en internet** del frontend web de Gemini como una herramienta `web_search`. Permite que clientes
MCP como Claude Desktop / Claude Code / Cursor «usen Gemini para buscar en internet» y devuelvan
una **respuesta sintetizada + enlaces a las fuentes**.

No hace falta levantar un proceso aparte ni un despliegue adicional: con arrancar el backend ya está. El transporte
es HTTP (Streamable HTTP), así que un cliente remoto solo necesita la URL y reutiliza el pool de cuentas / pool de
proxies / límite de tasa del backend. Se puede buscar de forma anónima, sin asociar cookies.

**Configuración del cliente** (ejemplo con `claude_desktop_config.json` de Claude Desktop):

```json
{
  "mcpServers": {
    "gemini-search": {
      "url": "http://tu-servidor:8083/mcp",
      "headers": { "Authorization": "Bearer sk-gemini-tu-key" }
    }
  }
}
```

- `url` apunta al `/mcp` del backend; si se ejecuta en local, es `http://localhost:8083/mcp`.
- `Authorization` lleva la misma API key que la interfaz de OpenAI (consúltala en la página «Ajustes» del panel).
- Claude Code: `claude mcp add --transport http gemini-search http://localhost:8083/mcp --header "Authorization: Bearer sk-gemini-tu-key"`.

Herramienta `web_search(query)`: se le pasa una consulta/pregunta y devuelve la respuesta de Gemini tras
verificarla en internet, con una lista de fuentes bajo `Sources:` al final. Actualmente es la única herramienta
(el `url_context` para leer una URL concreta aún no está implementado).

## Panel de administración

`http://localhost:8083/admin`, inicia sesión con `--admin-token`.

- **Resumen** — KPI de 24h + gráfico de tendencia de doble eje de volumen de peticiones / latencia P50 + estadísticas agrupadas por modelo/proxy + uso del límite de tasa por IP + diagnóstico de conectividad con un clic
- **Registro de peticiones** — lista de detalles (solo metadatos, sin contenido de prompt/response), filtros por estado/modelo + paginación
- **Pool de proxies** — alta/baja/modificación en caliente + activar/desactivar + disyuntor por número de fallos (cada proxy es un IP slot independiente)
- **Pool de cookies** — importa varias cuentas de Google con sesión iniciada; las peticiones rotan automáticamente dando **prioridad a la menos usada recientemente**. Con un clic puedes «comprobar» si cada cuenta sigue con sesión iniciada; **las cuentas invalidadas se marcan en rojo pidiendo reimportarlas**; se renueva automáticamente `__Secure-1PSIDTS` + keep-alive cada 10 minutos; cada cuenta queda fijada a su propia salida. La lista solo muestra un resumen anonimizado (nº de cookies / elementos clave / últimos 4 dígitos de SAPISID / nº de fallos)
- **Ajustes** — formulario de configuración en caliente (se aplica al guardar) + rotación de API Key + vista de solo lectura de la configuración de despliegue

El frontend del panel es un único HTML, con Chart.js embebido en el binario, **sin CDN**: funciona incluso en despliegues en red interna/offline.

**El reverse proxy a un subdirectorio no requiere configuración adicional**: todas las direcciones del panel son relativas;
reenviando `https://example.com/gemini/` a la `/` de este servicio, podrás abrirlo con `https://example.com/gemini/admin`.
(Acceder a `/admin` sin la barra final redirige con 301 a `admin/`; las direcciones relativas se resuelven según el
directorio de la URL del documento y ambas formas difieren en un nivel, así que conviene unificarlas para que no se resuelvan mal.)

## Modelos

El servidor del frontend web de Gemini solo reconoce tres modelos (la lista proviene de `batchexecute?rpcids=otAQ7b`):

| Modelo | Descripción |
|---|---|
| `gemini-3.6-flash` | Todoterreno, por defecto |
| `gemini-3.5-flash-lite` | Ultrarrápido, ligero |
| `gemini-3.1-pro` | El más potente, **requiere cookie**; cada respuesta incluye cadena de pensamiento |
| `gemini-3.8-flash` | El nuevo Flash, **requiere la cookie de una cuenta Gemini de pago** (las cuentas gratuitas se degradan a 3.5 Flash-Lite). `gemini-3.7-flash` es su alias: Google ascendió el hex de 3.7 a 3.8 en el mismo sitio |
| `gemini-3.6-flash-thinking` | 3.6 Flash con pensamiento extendido, **requiere cookie** |
| `gemini-3.5-flash-lite-thinking` | 3.5 Flash-Lite con pensamiento extendido, **requiere cookie** |
| `gemini-3.1-pro-thinking` | 3.1 Pro con pensamiento extendido, **requiere cookie** |
| `gemini-3.8-flash-thinking` | 3.8 Flash con pensamiento extendido, **requiere cookie de cuenta de pago** (`gemini-3.7-flash-thinking` es el alias) |
| `gemini-image` | Generación de imágenes (Nano Banana), salida en base64, **requiere cookie** |
| `gemini-music` | Música (Lyria, ~30 segundos), salida en base64, **requiere cookie** |
| `gemini-canvas` | Canvas, genera documentos HTML interactivos (devuelve un bloque ```html en línea), **requiere cookie** |
| `gemini-video` | Generación de vídeo (asíncrona, de decenas de segundos a varios minutos), salida MP4 en base64, **requiere cuenta Pro/de pago**; las cuentas gratuitas son rechazadas por la política de contenido del upstream |

Sin cookie configurada, `/v1/models` solo devuelve los dos primeros, y seleccionar `gemini-3.1-pro` da error directamente
explicando el motivo. Como una petición anónima se degrada inevitablemente y en silencio a 3.5 Flash-Lite, es mejor fallar
al elegir el modelo que darle al cliente una respuesta «correcta pero que en realidad no es Pro».

Con una cookie válida configurada, es **Pro de verdad**: en 6 peticiones seguidas el servidor reportó siempre `3.1 Pro`.

Los tres `-thinking` son el interruptor de «pensamiento extendido» de la UI web, ortogonal al modelo: los tres modelos
pueden activarlo, no son tres modelos adicionales. El nombre que reporta el servidor lleva `Extended` (p. ej.
`3.6 Flash Extended`), y la cadena de pensamiento se alarga notablemente (medido: 2467 / 1059 / 583 caracteres, frente a
0 / 0 / 268 en las versiones normales). **Solo surte efecto con sesión iniciada**: si una petición anónima incluye este
interruptor, el servidor lo ignora en silencio, por eso no se expone sin cookie.

Solo se exponen estos tres modelos base. Los antiguos `gemini-3.5-flash`, `gemini-3.5-flash-thinking`,
`gemini-3.5-flash-thinking-lite`, `gemini-auto` y `gemini-flash-lite` **se han eliminado**
(si se envían, devuelven 400): no tienen entrada correspondiente en el servidor y mantenerlos solo haría
creer que hay cinco modelos distintos disponibles.

> **`@think=N` está obsoleto.** Ese sufijo se escribía en `inner[17]` de la petición y siempre se tomó por
> «profundidad de pensamiento», pero la captura de tráfico demuestra que es el **índice de turno dentro de la sesión**
> (primer turno `[[0]]`, segundo turno con id de sesión `[[1]]`, incrementándose turno a turno), sin relación con
> la profundidad de pensamiento. Nosotros abrimos una sesión nueva cada vez, así que ese valor es siempre 0 y el
> parámetro nunca ha surtido efecto. El sufijo se sigue aceptando pero se ignora directamente, sin afectar al enrutado.

### Cadena de pensamiento (reasoning_content)

`gemini-3.1-pro` emite primero un fragmento de su propio razonamiento antes de cada respuesta; este proyecto lo expone
en el campo de facto estándar `reasoning_content`, que clientes como newapi, Cherry Studio y ChatBox renderizan como
«proceso de pensamiento» plegable. Los otros dos modelos no producen cadena de pensamiento, por lo que este campo no aparece.

Sin streaming:

```json
{"choices":[{"message":{
  "role":"assistant",
  "content":"The core rule of this classic puzzle is...",
  "reasoning_content":"**Defining the Constraints**

I've successfully defined..."
}}],
 "usage":{"prompt_tokens":26,"completion_tokens":337,"reasoning_tokens":77,"total_tokens":363}}
```

En streaming se emite por `delta.reasoning_content`, y **el cuerpo no empieza a emitirse hasta que se ha enviado
por completo el bloque de pensamiento** (ese es el orden del upstream); la experiencia del cliente es igual que en el frontend web.

`reasoning_tokens` **se cuenta aparte y no se incluye en `completion_tokens`**: la cadena de pensamiento se pliega
por defecto en el cliente y no se muestra, así que contarla haría que el usuario pagara por una salida que no ve
(newapi aguas abajo factura según `completion_tokens`). Quien quiera cobrarla, que sume ambos valores.

### Límites conocidos de capacidad

Las llamadas anónimas (sin cookie) solo obtienen los dos modelos de texto anteriores + la búsqueda en internet propia
de Gemini. `gemini-3.1-pro` se degrada en silencio a 3.5 Flash-Lite en modo anónimo, por eso directamente no se expone.

Asociar una cookie desbloquea además: `gemini-3.1-pro`, **la versión de pensamiento extendido de los tres modelos**,
**lectura de imágenes / vídeos**, **contexto más largo** (las conversaciones muy largas se convierten automáticamente
en un archivo de texto adjunto y se envían; ver más abajo), y **generación de imágenes (`gemini-image`)**,
**música (`gemini-music`)**, **canvas (`gemini-canvas`)** y **generación de vídeo (`gemini-video`, requiere cuenta Pro)**.

La investigación profunda sigue **sin implementar** (flujo asíncrono de varios pasos). La columna «modelo real» del
panel de administración indica qué modelo usó realmente el servidor, de modo que una degradación se detecta de un vistazo.

### Generación de imágenes / música

`gemini-image` (Nano Banana) y `gemini-music` (Lyria, ~30 segundos) pasan por `/v1/chat/completions` igual que un chat
normal; en el mensaje de user se escribe qué dibujar / qué pieza musical se quiere. Los bytes de la salida se colocan
en el `content` devuelto como **base64 data URL**: la imagen es `![image](data:image/png;base64,…)` y el audio
`[audio](data:audio/mpeg;base64,…)`. Los clientes compatibles con markdown pueden renderizar la imagen directamente;
para guardarla en un archivo, decodifica el trozo de base64 que va después de la coma.

```bash
curl http://127.0.0.1:8083/v1/chat/completions \
  -H "Authorization: Bearer sk-gemini-..." -H "Content-Type: application/json" \
  -d '{"model":"gemini-image","messages":[{"role":"user","content":"dibuja un gato naranja con casco de astronauta"}]}'
```

No convertir a enlace externo es intencionado: el enlace del resultado de Gemini solo se puede descargar con cookie,
y si se le da el enlace directamente al cliente no lo puede abrir, por eso el servidor lo descarga y lo convierte a
base64. El base64 del resultado **no se cuenta en `completion_tokens`** (de lo contrario, una imagen de un millón de
tokens haría que el cobro aguas abajo fuera disparatado); solo se cuenta el texto explicativo que acompaña al modelo.
Ambas requieren sesión iniciada; sin cookie, estos dos modelos no aparecen en `/v1/models`. Parámetros como `size` / `n`
no tienen control correspondiente en el upstream; si se envían, se ignoran.

**El contexto multiturno se implementa concatenando los `messages` en un único prompt** (el multiturno nativo del
protocolo web requiere un token que solo puede generar un runtime JS de navegador, imposible de fabricar con HTTP puro).
El precio es que se reenvía todo el historial en cada turno, y así se choca con el muro de longitud de la entrada única:
**unos 130 000 bytes UTF-8**; si se supera, el upstream **trunca en silencio por la cola y sin error** — y como el
mensaje más reciente va al final, lo que se come es justo lo que acabas de preguntar, con la apariencia de que
«el modelo se ha vuelto tonto de repente».

Con cookie, las conversaciones muy largas se convierten automáticamente en un adjunto `message.txt` y se suben,
esquivando el muro del cuerpo de la petición; **pero el adjunto tiene otro muro**: el contenido total que puede ver
el modelo es de unos **160 000 bytes**; lo que se pasa de ahí, aunque se suba, no se puede leer (medido: el total es
fijo y solo se desplaza el desfase: en 157 833 se lee, en 163 371 no; dividir en varios adjuntos no aumenta la cuota).
Así que con cookie la longitud disponible sube de 130 000 a unos 160 000; **las conversaciones realmente largas siguen
necesitando que el cliente las comprima por su cuenta**. Sin cookie, devuelve directamente 400 `context_length_exceeded`,
sin perder datos en silencio.

## Configuración

La configuración está en dos sitios, clasificados según «si el cambio requiere reinicio»:

### Configuración en caliente → página «Ajustes» del panel de administración

Al guardar **surte efecto de inmediato**, sin reiniciar. Se guarda en la base de datos y tiene prioridad sobre `config.json` y los argumentos de línea de comandos.

| Elemento | Descripción |
|---|---|
| Modelo por defecto | Cuál se usa cuando el cliente no envía `model` |
| Concurrencia / RPM / RPH por slot | Cuota del límite de tasa; 0 = sin límite |
| Límite de bytes del prompt | Si se supera: con cookie se convierte en adjunto de texto y se envía (longitud disponible de unos 160 000 bytes); sin cookie devuelve 400 `context_length_exceeded`. En ningún caso se trunca en silencio. Se calcula en bytes UTF-8 (el muro del upstream no tiene que ver con los tokens); por defecto 128000. 0 = sin límite |
| Multiturno (`multi_turn`) | El interruptor global decide si se usa la continuación nativa por conversation_id de Gemini del lado del servidor: el cliente reenvía todo el historial en cada turno, pero el servidor, al reconocer la continuación, solo envía la última frase (o los resultados de herramienta nuevos) y deja el historial en el servidor, de modo que las conversaciones largas ya no chocan con el muro de bytes de una sola petición. Disponible tanto con sesión iniciada como de forma anónima. **Activación consciente**: aunque el interruptor esté apagado, en una sesión **con herramientas y con cuenta con cookie** se usa igualmente la continuación, para no reenviar todo el historial en cada vuelta del bucle agéntico. Si el cliente no reproduce el historial de forma idéntica, la huella no coincide y cae a sesión nueva (se reenvía todo), sin regresión. **Atención**: no amplía la ventana de contexto del modelo; el contenido antiguo que se sale de la ventana sigue siendo expulsado. Lo que resuelve es «que las conversaciones largas no choquen con el muro + conservar el contexto reciente», no «alimentar documentos larguísimos». |
| Eliminar conversación automáticamente (`auto_delete_conversation`) | Desactivado por defecto. Al activarlo, tras cada resultado se elimina automáticamente la conversación que queda en gemini.google.com, para no acumular un montón en la cuenta con sesión iniciada. Solo surte efecto con sesión iniciada; asíncrono y best-effort; si falla el borrado, no afecta a la respuesta. |
| Priorizar anónimo (`anon_first`) | Desactivado por defecto. Al activarlo, las peticiones que no necesitan capacidades con sesión iniciada (texto plano, sin pensamiento, sin herramientas, sin imágenes) van de forma anónima y no ocupan cuentas con cookie, ahorrando cuota de cuenta; solo se elige cuenta cuando se usan 3.1 Pro / 3.8 Flash / pensamiento extendido / generación de imágenes / subida de archivos, etc. Si se desactiva, siempre que haya una cuenta en el pool se usa una cuenta. |
| Forzar herramientas en el primer turno (`force_tool_first_turn`) | Desactivado por defecto. Al activarlo, en el primer turno de una sesión con herramientas y con `tool_choice` automático o ausente, el proxy lo eleva a obligatorio para que el modelo empiece a actuar (listar/leer) en vez de responder en prosa. Útil con clientes agénticos y modelos débiles; puede forzar una llamada aunque el mensaje no la necesite. |
| Nº de reintentos / intervalo de reintento / timeout del upstream | |
| Días de retención de detalles | Al caducar solo se eliminan los detalles; los datos agregados se conservan permanentemente |
| Huella TLS | `chrome_146` (por defecto) / `chrome_144` / `chrome_133` / `firefox_147` / `safari_16_0` / `safari_ios_17_0` |
| Versión de `bl` de Gemini | Número de versión del frontend del upstream; cámbialo aquí cuando caduque |
| Imprimir logs de peticiones | |

Todos los valores se validan por rango en el backend (p. ej. `retry_attempts` solo acepta 1-10, el timeout 5-600 segundos);
los valores ilegales se rechazan indicando el motivo. Las restricciones del lado del navegador se pueden saltar con
facilidad; el verdadero control está en el servidor.

### Credenciales → también en el panel

| Elemento | Descripción |
|---|---|
| API Key | Se genera automáticamente en el primer arranque; en el panel se puede rotar o personalizar |
| Google Cookie | Se pega directamente en la página «Ajustes» del panel y surte efecto al guardar. Solo tras asociarla aparece `gemini-3.1-pro` en la lista de modelos |

Ambas se guardan en la base de datos. Los valores ya guardados no se muestran (de la cookie solo se muestra cuántas se han reconocido y si están los elementos clave).

### Configuración de despliegue → `docker-compose.yml`

Solo queda lo que exige reiniciar el proceso al cambiarlo:

| Elemento | Ubicación |
|---|---|
| Puerto de escucha | `ports` + `command: --port` |
| Ruta de la base de datos | `volumes` + `command: --db` |
| `ADMIN_TOKEN` | `environment`, token de inicio de sesión del panel |

La variable de entorno `API_KEY` bloquea la API key (no se puede cambiar desde el panel); se usa en despliegues donde no se quiere que se modifique en caliente.

`--proxy` y `--cookie-file` son **parámetros de siembra**, no una segunda configuración: al arrancar importan el valor al
pool de proxies / pool de cookies (deduplicando por URL y contenido de cookie) y a partir de ahí todo se gestiona desde el
panel. Al cambiarlos, reiniciar surte efecto.

Los argumentos de línea de comandos siguen disponibles; su función es la de sobrescritura temporal para depuración local. Prioridad:
**cambios del panel > CLI flag / `config.json` > valores por defecto integrados**.

Si ejecutas el binario directamente sin Docker, puedes copiar `config.example.json` a `config.json` como plantilla de
arranque (si no se pasa `--config`, se busca automáticamente `config.json` en el directorio actual y, en su defecto,
`$HOME/.config/gemini-web2api/config.json`). De los parámetros de ajuste que contiene, si se cambian desde el panel
prevalece el panel; `config.json` solo determina los valores iniciales del primer arranque.

Conjunto completo de argumentos de línea de comandos:

| flag | Descripción |
|---|---|
| `--port` | Puerto de escucha, por defecto 8083 |
| `--config` | Especifica la ruta de `config.json` |
| `--db` | Ruta de SQLite, por defecto `./data/gemini.db` |
| `--admin-token` | Token de inicio de sesión del panel; vacío = el panel no autentica (solo aceptable si se enlaza a 127.0.0.1) |
| `--api-key` | Bloquea la key de `/v1/*` (no se puede cambiar desde el panel); equivale a la variable de entorno `API_KEY` |
| `--cookie-file` | Al arrancar, importa las cookies del archivo al pool de cookies |
| `--proxy` | Al arrancar, importa este proxy al pool de proxies |
| `--impersonate` | Nivel de huella TLS |
| `--version` | Imprime la versión y sale (es lo que usa el healthcheck de Docker) |

## Cookie (opcional)

Tras asociar la cookie de una cuenta de Google, las peticiones usan la sesión iniciada y las capacidades adicionales son
**`gemini-3.1-pro` + cadena de pensamiento** (ver la sección «Cadena de pensamiento» arriba), **lectura de imágenes / vídeos**,
**generación de imágenes (`gemini-image`) / música (`gemini-music`) / canvas (`gemini-canvas`) / vídeo (`gemini-video`, requiere cuenta Pro)**.
Probado y funcional con cuentas gratuitas: 6 peticiones seguidas reportaron todas `3.1 Pro`.

La investigación profunda (asíncrona de varios pasos) sigue **sin implementar**.

> Las peticiones con cookie deben llevar además un token XSRF; este proyecto lo obtiene automáticamente de la página de
> Gemini, lo cachea por cookie y lo vuelve a obtener al caducar, sin configuración. (Si falta este paso, **todas** las
> peticiones dan 400; las anónimas, en cambio, no se ven afectadas: las versiones antiguas tropezaron con este problema.)

1. Inicia sesión en [gemini.google.com](https://gemini.google.com) desde el navegador
2. DevTools (F12) → Application → Cookies → `https://gemini.google.com`
3. Copia: `SID` / `HSID` / `SSID` / `APISID` / `SAPISID` / `__Secure-1PSID` / `__Secure-1PSIDTS`
4. Pega en el panel «Pool de cookies → Añadir cuenta»; o escríbelo en `cookie.txt` y arranca añadiendo
   `--cookie-file cookie.txt` (se importa al pool al arrancar y luego se gestiona desde el panel):
```
SID=...; HSID=...; SSID=...; APISID=...; SAPISID=...; __Secure-1PSID=...; __Secure-1PSIDTS=...
```

También acepta el formato JSON `{"cookie": "SID=...; ...", "sapisid": "..."}`. Las peticiones con `SAPISID` calculan
automáticamente la cabecera de autorización `SAPISIDHASH`, así que este elemento es imprescindible.

**La cookie solo tiene una vía de entrada: el «pool de cookies»**: cada petición elige una cuenta enabled dando prioridad
a la menos usada recientemente y, al elegirla, se avanza la rotación. Si hay cuentas enabled en el pool, `gemini-3.1-pro`
aparece en la lista de modelos.

El «nº de fallos / último éxito» de la cuenta se actualiza automáticamente con cada petición. El criterio es que **solo
401/403 cuentan como error de la cookie**: los errores de red, los fallos de proxy y los bloqueos de Google (302) no
cuentan en absoluto. Los proxies residenciales tienen una tasa de degradación de salida muy alta, y contabilizarlos
convertiría el nº de fallos en ruido del proxy, registrando como la que más falla una cookie buena.

Ojo: «último éxito» solo indica que una petición en la que participó esta cookie tuvo éxito; **no significa que la cookie
siga siendo válida**: tras caducar, Gemini no da error, simplemente te trata como usuario anónimo. Para confirmar la
validez, pulsa el botón «comprobar» de la lista; distingue «sesión válida» de «caducada/no válida» sin necesidad de enviar
una conversación real para probar.

**La cookie se renueva automáticamente, sin que tengas que preocuparte**. Dos cosas a la vez:

- Casi todas las respuestas del upstream refrescan `SIDCC` / `__Secure-1PSIDCC` / `__Secure-3PSIDCC`; las recibimos y las escribimos de vuelta en la cuenta.
- Cada 10 minutos (con un primer refresco 15 segundos después de arrancar) se envía un keep-alive a
  `accounts.google.com/RotateCookies`: primero se cambia un payload centinela por un `__Secure-1PSIDTS` (el ticket que
  caduca a los ~30 minutos; si no se refresca, te tratan como anónimo), y luego se refresca SIDCC por la vía del iframe
  del navegador. No se repite para la misma cuenta en 60 segundos, para evitar el 429.

> **Exporta las cookies con Firefox.** Las versiones nuevas de Chrome activan Device Bound Session Credentials, y la
> sesión exportada queda vinculada a este dispositivo; `*PSIDTS` no se puede renovar y muere en unas media hora o pocas
> horas. Firefox no tiene esta capa de vinculación y el mismo keep-alive puede renovarse indefinidamente. Si usas Chrome:
> abre una ventana de incógnito totalmente nueva, inicia sesión, exporta de inmediato y **cierra esa ventana enseguida**;
> no dejes que el navegador vuelva a rotar la misma sesión por su cuenta.

**Cada cuenta queda fijada a su propia salida.** Si el pool de cookies y el pool de proxies rotan de forma independiente,
la misma cuenta de Google enviaría peticiones desde decenas de IP distintas, lo cual a ojos de Google es la característica
típica de una cuenta compartida. La cuenta se vincula a la salida que use por primera vez y solo cambia cuando esa salida
deja de estar disponible.

**Si una cuenta falla, se pasa automáticamente a la siguiente**, sin que toda la petición se vaya al traste; de lo
contrario, cuanto más grande es el pool, más fácil es pisar una mina.

## Pool de proxies (el núcleo de la ruta gratuita)

**Por qué es necesario**: cuando una sola IP recibe peticiones en ráfaga hasta cierto número, es redirigida a
`google.com/sorry/index`. Ese número es **80-180**, con un rango amplio, y lo determinan conjuntamente la **estrategia de
conexión, la calidad de la salida y el ritmo de las peticiones**:

| Salida | Estrategia de conexión | Concurrencia | Ritmo | Peticiones exitosas hasta el bloqueo |
|---|---|---|---|---|
| Residencial | Reutiliza pool de conexiones | 10 | Sin intervalo | 151 / 172 / 177 |
| Residencial | Reutiliza pool de conexiones | 3 | Sin intervalo | 103 / 111 |
| Residencial | Nueva conexión cada vez | 10 | Sin intervalo | 106 / 109 |
| Residencial | Nueva conexión cada vez | 1 | Intervalo 24s | 81 / 166 |
| Estática | Reutiliza pool de conexiones | 10 | Sin intervalo | 188 |
| Estática | Reutiliza pool de conexiones | 1 | **10 veces/minuto** | **800 sin ser bloqueada** |

El criterio solo reconoce el 302 → `/sorry/`; las salidas están todas precribadas.

**La reutilización de conexiones aporta ~60%**: con la concurrencia fijada en 10 y ambos brazos arrancando a la vez, en
una prueba de solo 80 segundos (tan corta que la salida no tiene tiempo de derivar), reutilizar conexiones dio 172/177
y crear una nueva cada vez, 106/109.

**Un ritmo suave es más eficaz que cualquier otra cosa**: la misma IP estática, bloqueada a las 188 peticiones en ráfaga,
pasó a **800 peticiones seguidas a 10 por minuto, a lo largo de 110 minutos, sin ser bloqueada ni una vez**. Por eso el
`per_ip_rph=80` por defecto es un límite inferior muy conservador; los despliegues que claramente vayan a ritmo bajo
pueden subirlo mucho.

> Antes aquí decía «bajar el ritmo no sirve», con el criterio de que en salidas residenciales el rango de ráfaga 103-177
> y el rango lento 81-166 se solapaban casi por completo. Esa observación era correcta, pero la atribución estaba
> equivocada: las salidas residenciales se degradan solas al cabo de un rato (de 8 salidas precribadas limpias a ritmo
> lento, 6 superaron el 40% de fallos a mitad de camino), y la degradación eclipsaba el efecto del ritmo. Tras cambiar a
> IP estática y eliminar ese factor de confusión, el efecto del ritmo es evidente.

**Los fallos de proxy consumen cuota por adelantado**: cuanto más sucia es la ruta, antes llega el límite, porque parte
de esas peticiones «fallidas» en realidad ya llegaron a Google y se contabilizaron (en el rango lento se enviaron
realmente 195 peticiones para conseguir 166 éxitos, un consumo real 17% mayor). No esperes exprimir más capacidad
reintentando peticiones fallidas.

**Tras el bloqueo, este es duro y se recupera automáticamente en unas dos horas.** En dos repeticiones independientes,
cada una con 30 sondeos, intervalo de 20s y unos 10 minutos de duración, en total 60 sondeos con cero éxitos; siguiendo
sondeando, se recuperó entre los **106 y 121 minutos**. El enfriamiento del disyuntor del pool de proxies, fijado por
defecto en 120 minutos, se basa en esto.

En comparación: 10 IP con 50 peticiones cada una (418 peticiones) dieron cero rechazos de Google. El `per_ip_rph=80` por
defecto cae en el borde inferior del rango medido en el modo ráfaga.

**Cómo solucionarlo**: añade varios proxies en la página «Pool de proxies» del panel de administración; **cada proxy es
un IP slot independiente** y disfruta de su propia cuota de concurrencia/RPM/RPH. N proxies = N veces la capacidad total.

Protocolos de proxy compatibles:
- `http://user:pass@host:port`
- `https://user:pass@host:port`
- `socks5://user:pass@host:port`
- `socks5h://user:pass@host:port` (resolución DNS remota, para esquivar la contaminación DNS local)

**Reglas de programación automática**:
- Una vez configurados los proxies, **no se vuelve a la conexión directa** (para evitar que, al llenarse los proxies, la IP del host también se sature)
- Se dispara el disyuntor automáticamente tras 5 fallos (se puede restablecer manualmente desde el panel de administración)
- Todos los proxies llenos → devuelve HTTP 429 (no consume cuota de Google; espera a que quede un hueco y reintenta)

**No lee las variables de entorno `HTTPS_PROXY` / `ALL_PROXY`.** Los proxies solo se toman del pool de proxies
(`--proxy` / `config.json` solo siembran el pool al arrancar); de lo contrario, una variable exportada al descuido en el
host cambiaría en silencio la IP de salida, mientras el panel seguiría mostrando conexión directa, y se diagnosticaría mal.

## Simulación de huella

El caso de conexión directa (sin proxy) usa [bogdanfinn/tls-client](https://github.com/bogdanfinn/tls-client); el handshake TLS + los frames SETTINGS de HTTP/2 + ALPS se alinean por completo con el Chrome 146 real. Desde la perspectiva del control de riesgos de Google, es indistinguible de un navegador real.

En el caso con proxy se usa stdlib `net/http` + `http.ProxyURL` (la mejor compatibilidad), pero las cabeceras de capa de aplicación (`Sec-CH-UA` / `Sec-Fetch-*` / `User-Agent`) siguen disfrazadas con los valores reales de Chrome 146.

**Atención: al usar proxy, la huella TLS es la nuestra, no la del nodo de salida.** HTTP CONNECT solo crea el túnel; el
TLS lo negociamos de extremo a extremo con Google. Eco de JA3 medido: bajo el mismo proxy, el JA3 de stdlib y el de
tls-client son distintos (el proxy no los reescribe), mientras que el JA3 de stdlib en directo y a través del proxy es
idéntico (la capa de proxy no interviene en el TLS). Por eso, **con proxy configurado, lo que se expone a Google es la
huella de la biblioteca estándar de Go, no la de Chrome 146**.

Pero esto no afecta al umbral de bloqueo: en la comparación arrancada a la vez, tls-client Chrome_146 llegó a 111
peticiones y Go stdlib a 103, una diferencia de 8, mientras que con la misma configuración la varianza entre salidas
puede llegar al 36% (151 vs 111). Para querer que la ruta con proxy también use la huella de Chrome hacen falta otras
razones (por ejemplo, preocupación por el perfil de la cuenta a largo plazo); no se puede esperar ganar capacidad con ello.

**Hallazgo inesperado**: una llamada ingenua con SDK (por ejemplo, Python urllib) obtiene **HTTP 429** al disparar el
control de riesgos; tras disfrazarnos de Chrome, el control de riesgos devuelve **HTTP 302** con redirección a
`google.com/sorry/index` (página de verificación CAPTCHA). En esencia, ambos son listas negras de IP, pero el 302
demuestra que Google realmente nos tomó por un navegador.

## Privacidad

- **El prompt y el contenido de la response nunca se almacenan en la base de datos**: solo se guardan metadatos: modelo, ID de proxy, latencia, nº de tokens, código de estado, tipo de error
- Las columnas históricas `prompt_preview` / `response_preview` se eliminan (DROP) automáticamente al migrar desde versiones antiguas
- El nº de tokens se calcula con precisión con el tokenizador BPE cl100k_base de [tiktoken-go](https://github.com/pkoukk/tiktoken-go) (preciso tanto en chino como en inglés), no con una estimación `len/4`
- El frontend web de Gemini no devuelve el nº real de tokens (medido: la respuesta no contiene ningún campo token/usage; solo se puede estimar en local)

## Cobertura del protocolo OpenAI

| Ruta | Estado | Observaciones |
|---|---|---|
| `POST /v1/chat/completions` | ✅ | Streaming real: cada frame que emite el upstream se reenvía como delta (medido: una respuesta en chino de 400 caracteres produce 40 chunks). La secuencia de chunks es `delta{role}` → `delta{content}`×N → `delta` vacío + `finish_reason` → `[DONE]`. **Con `tools` se bufferiza a propósito**: se recibe la respuesta completa, se validan/corrigen los `tool_call` (nombres por sinónimo/errata, argumentos contra el esquema) y se reintenta una vez si el `tool_choice` no se cumplió o los argumentos no validan; solo entonces se emite el cuerpo y las llamadas. El bloque tool_call necesita el texto completo para parsearse |
| `POST /v1/responses` | ✅ | API de OpenAI Responses (la usa Codex CLI). **Sin streaming real**; sigue recibiendo todo y luego enviando según la secuencia de eventos |
| `GET /v1/models` | ✅ | 2 en modo anónimo; el tercero solo aparece con cookie asociada |
| `GET /` | ✅ | Health check, sin autenticación, devuelve status/version/models |
| `/v1beta/models/…` (formato nativo de Gemini CLI) | ❌ | Sin implementar; solo se exponen interfaces con forma OpenAI |
| `/v1/embeddings`, `/v1/images/*`, `/v1/audio/*` | ❌ | Sin implementar, devuelve 404 |
| Function calling | ✅ | Implementado a **nivel de prompt** (el frontend web no expone una capa de protocolo): el modelo emite bloques ` ```tool_call``` ` y se parsean de forma tolerante (JSON suelto, aliases, `arguments` como objeto o cadena; un bloque no interpretable se devuelve como texto en vez de perderse). Un nombre **no declarado** se corrige al más probable (sinónimo conocido —`shell_command`→`bash`, `cat`→`read`, etc.— o errata cercana) antes de emitirlo. Los turnos con `tools` se **bufferizan**: se validan/corrigen los argumentos contra el `parameters` de la herramienta y se emiten al final; `parallel_tool_calls` admite varias llamadas por turno. Si la salida se corta a mitad de un bloque, el `finish_reason` es `length` (o `incomplete` en Responses) en vez de `stop`. **Fiable para consultar datos privados/sistemas internos**; lo que Gemini responde por sí mismo (el tiempo) lo contesta directamente, y las acciones con efectos secundarios (enviar correos) se rechazan. Clientes agénticos (Codex, opencode, Cursor) funcionan. Límites: no es capa de protocolo real, el modelo no siempre acierta el formato y hay tope de ~130 KB por petición |
| `tool_choice` | ⚠️ | `none` no inyecta las definiciones de herramientas; `required`/una función concreta añaden una formulación forzada. A nivel de prompt no se puede forzar de verdad; para acercarse, si tras el turno buffereado no hay llamada válida (o los argumentos no validan) se hace **un único reintento correctivo** con recordatorio reforzado, y se adopta solo si resuelve. En el chat normal sin herramientas no se reintenta (streaming real: los bytes ya se enviaron) |
| `stream_options.include_usage` | ✅ | Añade tras `finish_reason` un chunk de usage con `choices` vacío |
| Nº de tokens de `usage` | ✅ | tiktoken cl100k_base, con el mismo criterio que la tabla requests del panel de administración |
| `n` > 1 | ❌ | Devuelve 400. El upstream solo da un candidato; procesarlo en silencio como 1 haría que el cliente recibiera menos resultados |
| Parámetros de muestreo | ➖ | `temperature` / `top_p` / `max_tokens` / `stop` / `seed` / `presence_penalty` / `frequency_penalty` **se aceptan pero se ignoran, sin error**. El protocolo web de Gemini no tiene estos controles |
| `response_format` / `logprobs` | ➖ | Sin implementar; se aceptan pero se ignoran |
| Vision / entrada de imágenes | ⚠️ | **Disponible con cookie**: acepta tanto `image_url` (chat) como `input_image` (responses), admite URL `data:` y enlaces http(s), con un máximo de 12MB por imagen. En modo anónimo devuelve 400: de forma anónima **sí** se puede subir la imagen (`content-push.googleapis.com/upload/`, resumable en dos pasos), pero el upstream rechaza su referencia en la conversación (`BardErrorInfo 1100`) |
| Audio | ❌ | Enviar `input_audio` devuelve 400. El frontend web tiene generación de música (Lyria 3), que requiere sesión iniciada |

## Estructura del proyecto

```
main.go                    solo una línea app.Run(); la entrada se queda en el directorio raíz, `go build .` funciona directamente
internal/app/              toda la implementación
  app.go                   parseo de flags + registro de rutas + arranque
  config.go                carga de configuración + valores por defecto
  client.go                doble client: tls-client (chrome146) + stdlib (con proxy)
  gemini.go                tabla de modelos + payload de 80 slots + header de modelo + StreamGenerate + parseo de wrb.fr
  xsrf.go                  XSRF token necesario con cookie: obtención + caché por cookie + autocuración al caducar
  messages.go              OpenAI messages → prompt, parseo de tool_call
  server.go                /v1/* + entrada del límite de tasa + validación de parámetros + escritura de metrics
  sse.go                   escritor SSE (envía el header de forma perezosa; si falla, aún puede devolver un JSON 502)
  ratelimit.go             límite de tasa independiente de concurrencia / RPM / RPH por IP slot
  tokenizer.go             singleton de tiktoken cl100k_base
  apikey.go                API key (doble vía: bloqueada por argumento de arranque / rotable desde el panel)
  db.go                    esquema SQLite: sessions / requests / accounts / kv
  proxy.go                 CRUD del pool de proxies + programación de capacidad + disyuntor
  cookie_pool.go           capa de datos del pool de cookies (CRUD + selección por menos usada recientemente + reescritura de salud + fusión de elementos de refresco)
  rotate.go                keep-alive de sesión (RotateCookies: centinela 1PSIDTS + iframe SIDCC, intervalo indicado por el servidor)
  upload.go                subida de adjuntos (content-push resumable en dos pasos)
  context_file.go          conversión de conversaciones muy largas en adjunto de texto
  vision.go                entrada de imágenes: data: URL / enlaces http(s) → adjunto pendiente de subir
  bl.go                    seguimiento automático del número de versión bl del frontend del upstream
  scheduler.go             agregación horaria/diaria + limpieza de detalles caducados
  runtime.go               instantánea de configuración en caliente (los cambios del panel surten efecto al instante)
  admin.go                 autenticación de /admin/api/* + REST
  admin_cookies.go         REST de admin del pool de cookies (anonimizado antes de devolver)
  admin_ui.go              embed de admin_ui/
  admin_ui/                frontend del panel de administración (página única + Chart.js, empaquetado con el binario, sin CDN)
  gemini_test.go           pruebas unitarias de la capa de protocolo: slots del payload, parseo de wrb.fr, control de modelos, límite de tasa
Dockerfile                 compilación multi-etapa (builder alpine → runtime distroless)
docker-compose.yml         contenedor único, por defecto descarga la imagen de ghcr, monta un volume para sqlite
.github/workflows/         docker.yml empuja la imagen / release.yml etiqueta y publica el binario
```

## Limitaciones

- **Límite por IP**: en ráfaga, medido, tras **80-180 peticiones** se redirige a la página sorry (la reutilización de conexiones permite ~60% más: con concurrencia 10, reutilizar 172/177, nueva cada vez 106/109). Pero **a ritmo suave casi no se alcanza**: en una IP estática, 800 peticiones seguidas a 10 por minuto sin ser bloqueada. El `per_ip_rph=80` por defecto toma el borde inferior del rango → para ampliar capacidad, configura un pool de proxies, o ejecuta a ritmo bajo y sube el límite
- **Funciones con sesión iniciada**: la generación de imágenes (`gemini-image`), música (`gemini-music`) y canvas (`gemini-canvas`) están implementadas; el vídeo y la investigación profunda no lo están (el vídeo lo retiró Google para las cuentas gratuitas, y la investigación profunda es un flujo asíncrono de varios pasos)
- **Function calling**: a nivel de prompt (la capa de protocolo real de OpenAI no está a nuestro alcance); tolerante con formatos sueltos, pero el modelo no siempre acierta el formato
- **Multimodal**: la lectura de imágenes/vídeos requiere cookie; la generación de imágenes/música/canvas/vídeo está disponible con cookie, y el vídeo además requiere cuenta Pro/de pago
- **El contexto largo tiene dos muros**: el cuerpo de la petición, unos 130 000 bytes, y el adjunto, unos 160 000 bytes (este último es la **cantidad total** de contenido que puede ver el modelo; dividirlo en varios adjuntos no aumenta la cuota). La cookie solo sube la longitud disponible de 130 000 a unos 160 000; las conversaciones realmente largas siguen necesitando que el cliente las comprima por su cuenta
- **Nº de tokens**: se estima con tiktoken (el tokenizador real de Gemini no es público), con una desviación de ±20% respecto al valor real
- **El pool de cookies no elimina automáticamente las cuentas malas**: el resultado de las peticiones se reescribe (solo 401/403 cuentan como error de la cookie; los errores de red y las intercepciones 302 no cuentan), pero al alcanzar cierto número de fallos no se desactivan automáticamente; hay que detenerlas a mano desde el panel. Además, `last_ok_at` solo indica que «una petición en la que participó esta cookie tuvo éxito», no que siga siendo válida: tras caducar, Gemini no da error, solo te trata como usuario anónimo, y las peticiones de texto plano siguen dando 200
- **Las cookies exportadas desde Chrome puede que no se renueven**: las Device Bound Session Credentials de las versiones nuevas de Chrome vinculan la sesión al dispositivo, y el canje de `__Secure-1PSIDTS` da 401. Exportando con Firefox se puede alargar la vida a largo plazo
- **Bufferizado de los turnos con herramientas**: las peticiones de chat con `tools` y `/v1/responses` reciben todo y luego envían, a propósito, para poder validar/corregir/reintentar antes de emitir; solo el streaming de chat normal es un delta real

## Solución de problemas

| Síntoma | Causa probable | Qué hacer |
|---|---|---|
| Devolvemos **429** | La concurrencia/RPM/RPH de todos los IP slot está agotada; **no es un rechazo de Google** y no se consumió cuota del upstream | Añade proxies, o sube los límites en la página «Ajustes» |
| El diagnóstico del panel muestra **302 → `google.com/sorry/index`** | Esta IP de salida fue bloqueada por Google (tras 80-180 peticiones, según la estrategia de conexión y la calidad de la salida) | Cambia de salida / añade proxies. **Es un bloqueo duro, no probabilístico** (tras el bloqueo, 60 sondeos con cero éxito); reintentar sin cambiar de salida no sirve de nada |
| Respuestas vacías esporádicas, el panel las registra como rechazo del upstream | Rechazo momentáneo del upstream (`1155`), sin umbral predecible, independiente de la frecuencia/concurrencia/número acumulado | Reenviar una vez suele bastar. **Bajar el RPM no lo resuelve**; medido, no tiene relación con la frecuencia |
| Todas las peticiones dan timeout | No hay conectividad del equipo local a `gemini.google.com` | Configura un proxy (panel «Pool de proxies» o `--proxy`). Ojo: **no lee la variable de entorno `HTTPS_PROXY`** |
| Sale al arrancar, con `unable to open database file (14)` | El contenedor se ejecuta como nonroot(uid 65532) y el directorio anfitrión del bind mount pertenece a root, así que no puede escribir | Usa un volumen con nombre (el compose por defecto ya lo es), o `sudo chown -R 65532:65532 ./data` |
| Seleccionar `gemini-3.1-pro` da error directamente | Sin cookie configurada no se expone; es intencionado | Tras asociar una cookie (panel «Ajustes» o «Pool de cookies») ya se puede usar |
| Tras asociar una cookie, todas las peticiones dan 502 | La cookie ya no es válida y no se puede obtener el token XSRF | Vuelve a exportar la cookie. Criterio: si al pedir `gemini-3.1-pro` reporta 3.5 Flash-Lite, es que ha caducado |
| La cookie caduca en unos media hora y el keep-alive no sirve | Sesión vinculada al dispositivo de Chrome; el canje de `__Secure-1PSIDTS` da 401 | Inicia sesión de nuevo con **Firefox** y vuelve a exportar; al pulsar «keep-alive» en el panel deberías ver que se refrescó `__Secure-1PSIDTS` |
| El panel no abre / 401 | `--admin-token` (o `ADMIN_TOKEN`) no coincide | Si el token está vacío no hay autenticación; solo es aceptable si se enlaza a 127.0.0.1 |

Con el Docker en la red bridge por defecto, el upstream puede devolver contenido vacío (Google rechaza ciertos rangos NAT). Este proyecto **nunca lo ha reproducido**; si te ocurre, prueba `network_mode: host` para verificar si esa es la causa.

## Preguntas frecuentes (FAQ)

### ¿En qué se diferencia de la API oficial de Gemini de Google? ¿Necesito una API Key?

No. Este proyecto no encapsula `generativelanguage.googleapis.com`, sino que hace reverse proxy directamente del protocolo
del frontend web de gemini.google.com, por lo que no hay API Key ni cuota de pago. El precio es que los límites de capacidad
siguen al frontend web: lo que tiene el frontend web (búsqueda en internet, generación de imágenes, música, canvas, vídeo)
aquí se puede usar, y lo que no tiene (investigación profunda) aquí tampoco. De forma anónima (sin cookie) ya se pueden usar
los modelos de texto.

### ¿Qué relación tiene con Sophomoresty/gemini-web2api (Python) y otros proyectos Go llamados gemini-web2api?

Este repositorio es una implementación independiente en Go; en los primeros tiempos, al explorar el protocolo web, se tomó
como referencia el enfoque de la versión en Python (ver los enlaces al final), pero **no es un port**; otros proyectos Go con
el mismo nombre no tienen relación con este repositorio. Características propias de este repositorio: binario único, utls
que imita la huella TLS real de Chrome, pool de proxies + pool de cookies (renovación automática, cuentas fijadas a su salida),
con panel de administración, SQLite / MySQL / PostgreSQL, interfaz de vídeo asíncrona `/v1/videos` y herramienta MCP `web_search`.

### ¿Por qué la cookie caduca en unos media hora? ¿Cómo usarla a largo plazo?

El ticket que caduca es `__Secure-1PSIDTS` (caduca a los ~30 minutos). Este proyecto lo canjea periódicamente con un payload
centinela y hace keep-alive; **una cookie exportada tras iniciar sesión con Firefox se puede renovar indefinidamente**. Las
versiones nuevas de Chrome activan las sesiones vinculadas al dispositivo (DBSC), y la cookie exportada da 401 al canjearse,
por lo que una cuenta exportada desde Chrome muere en media hora o pocas horas; basta con exportarla con Firefox, sin
necesidad de extensión de navegador ni de máquina siempre encendida.

### ¿Me pueden banear la cuenta? ¿El bloqueo es por cuenta o por IP?

Por **IP de salida + huella TLS**, no por cuenta. Medido: en una misma salida, 80-180 peticiones en ráfaga provocan la
redirección a la página sorry, y se recupera automáticamente en unas dos horas; a ritmo suave (por ejemplo, 10 por minuto),
800 peticiones seguidas sin ser bloqueada. Pagar (Google AI Pro) **no** eleva ese umbral. Para ampliar capacidad, reparte las
peticiones entre varias salidas con un pool de proxies, no cambies de cuenta.

### ¿En qué se diferencian las cuentas gratuitas de las Pro / de pago?

Anónimo (sin cookie): `gemini-3.6-flash`, `gemini-3.5-flash-lite`, con búsqueda en internet.
Con la cookie de una cuenta gratuita: desbloquea además `gemini-3.1-pro`, la versión de pensamiento (`-thinking`) de los
tres modelos, lectura de imágenes / vídeos, generación de imágenes, música y canvas.
Solo en cuentas de pago: `gemini-3.8-flash` (las cuentas gratuitas se degradan a 3.5 Flash-Lite) y generación de vídeo
(`gemini-video` / `/v1/videos`).

### ¿Por qué cada vez que envío un mensaje aparece una conversación más en gemini.google.com?

Por defecto, cada petición es una conversación independiente. Tras marcar **continuación multiturno del lado del servidor**
(`multi_turn`) en la página «Ajustes» del panel, una misma secuencia de conversación reutiliza la misma conversación web:
solo se envía la última frase y el historial se queda en el servidor. Además, aunque el interruptor esté apagado, las
sesiones **con herramientas y con cookie** usan la continuación automáticamente (así el bucle agéntico no reenvía todo el
historial en cada turno). Si no quieres acumular conversaciones en el frontend web, marca **eliminar conversación web
automáticamente** (`auto_delete_conversation`), que la borra en cuanto se obtiene el resultado.

### ¿Puede servir de backend para herramientas agénticas como Cursor / Dify / WorkBuddy?

Se puede conectar (compatible con OpenAI) y no hay problema para chat puro y escritura de código; pero el frontend web
**tiene un tope de unos 130 000 bytes por petición** (unos 160 000 con cookie), y cuando una herramienta agéntica mete de
golpe decenas de miles de caracteres de prompt de sistema e historial, es fácil que responda cosas que no vienen al caso y
parezca volverse tonta. Es una limitación innata del modelo del frontend web, no un problema de la forma de conectarse.
Function calling es una implementación a nivel de prompt y el modelo no siempre devuelve el formato.

Configuración de opencode como cliente agéntico (`~/.config/opencode/opencode.json`):

```json
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "gemini-proxy": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "http://localhost:8083/v1", "apiKey": "sk-…" },
      "models": { "gemini-3.1-pro": { "name": "Gemini 3.1 Pro", "tool_call": true } }
    }
  },
  "model": "gemini-proxy/gemini-3.1-pro"
}
```

`tool_call: true` hace que opencode envíe el array `tools`; el proxy inyecta las instrucciones de
herramientas por su cuenta, así que **no** hace falta añadir prompts propios de herramientas.

**Lectura y escritura de archivos**: el I/O lo hace opencode (tools `read`/`write`/`edit`/`glob`/`grep`); el proxy solo transporta sus `tool_calls`. El proxy tolera el JSON suelto que suelen emitir los modelos al escribir (saltos de línea crudos, comillas sin escapar) y reemite `arguments` siempre como JSON válido. Ojo: la longitud de una escritura la limita la salida del frontend web, así que para archivos grandes conviene `write` y luego varios `edit`.

### ¿Existe gemini-3.8-flash?

Sí (`gemini-3.8-flash` y `gemini-3.8-flash-thinking`), requiere una cuenta Gemini de pago; si una cuenta gratuita lo
solicita, el servidor la degrada a 3.5 Flash-Lite. `gemini-3.7-flash` se conserva como alias y los clientes antiguos no
se ven afectados.

### ¿Qué modelo usa la generación de imágenes? ¿Puede sacar 2K?

`gemini-image` usa el puesto de herramienta de generación de imágenes del frontend web y no fija el modelo: lo que use
actualmente el frontend web como backend de generación (p. ej. Nano Banana 2), aquí se sigue automáticamente. La resolución
está fijada por el frontend web en unos 1 millón de píxeles; no hay 2K.

### ¿Por qué detrás de Dify / un reverse proxy el streaming se convierte en una salida de una sola vez?

El chat normal es streaming real (cada frame del upstream se reenvía); las peticiones con `tools` y `/v1/responses` se
bufferizan a propósito (reciben todo, validan/corrigen y luego envían). La respuesta ya lleva `X-Accel-Buffering: no`; si tras
pasar por un reverse proxy como Nginx se sigue almacenando en búfer, revisa los ajustes de búfer del propio reverse proxy;
este proyecto, en conexión directa, emite por segmentos.

### ¿Soporta MySQL / PostgreSQL?

Sí. Define la variable de entorno `SQL_DSN` (`mysql://user:pass@host:3306/db` o
`postgres://user:pass@host:5432/db?sslmode=disable`); si no se define, SQLite por defecto. La creación de tablas es
automática y las tres bases comparten un mismo esquema. Para un solo equipo, lo más sencillo es SQLite; MySQL / PG solo
hace falta cuando varias instancias comparten un mismo pool.

## Agradecimientos

- [bogdanfinn/tls-client](https://github.com/bogdanfinn/tls-client) — biblioteca TLS con huella real de Chrome
- [pkoukk/tiktoken-go](https://github.com/pkoukk/tiktoken-go) — BPE tokenizer
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) — SQLite en Go puro (CGO-free, se compila directamente en alpine)

## License

MIT — ver [LICENSE](LICENSE)

## Enlaces relacionados

- [Sophomoresty/gemini-web2api](https://github.com/Sophomoresty/gemini-web2api) — implementación similar en Python; en los primeros tiempos, al explorar el protocolo web de Gemini, se tomaron ideas de su enfoque
- [LINUX DO](https://linux.do) — este proyecto se comparte en esa comunidad

[![LinuxDo](https://img.shields.io/badge/%E7%A4%BE%E5%8C%BA-LinuxDo-blue?style=for-the-badge)](https://linux.do/)

