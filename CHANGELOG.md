# Registro de cambios

Este proyecto sigue [Versionado semántico](https://semver.org/lang/zh-CN/).

## 4.20.2

### Correcciones

- **El seguimiento automático de bl se adapta al cambio de nombre del servicio upstream**. En 2026-10
  el upstream renombró el servicio bl de `assistant-bard-web-server`
  a `gemini-web-uiserver` (actualmente `boq_gemini-web-uiserver_20261007.01_p0`). La regex de
  validación antes fijaba el nombre del servicio, así que tras el cambio de nombre el seguimiento
  automático rechazaba el nuevo valor capturado por "forma incorrecta" y volvía siempre al bl viejo
  de la configuración. Se relajó para validar solo la forma general
  `boq_<nombreServicio>_<fecha>.<versión>_p<número>`, dejando que el nombre del servicio cambie
  libremente, y el valor de reserva también se actualizó a la fecha actual. Los bl viejo y nuevo
  siguen funcionando en pruebas reales (la ruta del endpoint no cambió), así que no afecta a los
  despliegues en uso; solo restaura el funcionamiento del seguimiento automático.

## 4.20.1

### Correcciones

- **Se añade a RPH la visualización "0=sin límite", alineándola con concurrencia / RPM**
  (issues #32 #33). Tanto concurrencia como RPM ya aplicaban 0=sin límite (aparece en el hint de
  la página de ajustes y, en el resumen "distancia a la línea roja de bloqueo", el uso se muestra
  como ∞), pero en RPH faltaban ambos sitios: el hint no lo decía y la línea note del resumen no
  listaba RPH, así que al ponerlo a 0 no se veía que fuera sin límite. Completado. Es solo
  visualización; el backend ya era consistente en los tres.

## 4.20.0

### Añadido

- **Cuentas invalidadas del pool de cookies marcadas en rojo** (issue #18). Cuando la
  autenticación de una cuenta falla (`fail_count>0`, es decir 401/403), la columna de estado del
  panel la marca en rojo como "posiblemente inválida", la fila completa se pinta con fondo rojo y
  barra roja a la izquierda, y el resumen superior muestra "N posiblemente inválidas", para ver de
  un vistazo cuáles hay que reexportar con Firefox. La "detección" manual que descubre una
  invalidez la pone en rojo de inmediato, y al pasar la detección o tras usarla con éxito vuelve
  automáticamente a normal. (El mini navegador embebido que también se pedía en #18 no se hace,
  porque choca con el diseño de binario único.)

## 4.19.0

### Añadido

- **Renovación automática de cookies** (issue #6, fusionando el #25 de @s1oz). Antes, el keepalive
  `RotateCookies` cada 10 minutos usaba la ruta `[658, session_id]` del iframe del navegador y solo
  refrescaba `SIDCC` / `*PSIDCC`, **sin poder cambiar** `__Secure-1PSIDTS` —que es justo el ticket
  de vida corta que caduca a los ~30 minutos, por eso la cuenta logueada parecía morir a la media
  hora y el botón de keepalive tampoco la rescataba. Ahora primero se usa el payload centinela
  `[000,"-0000000000000000000"]` (que solo lleva el subconjunto de dos entradas `__Secure-1PSID` +
  `__Secure-1PSIDTS`; añadir más provoca 401) para canjear un nuevo `*PSIDTS`, y después se ejecuta
  el keepalive de SIDCC original; tras importar se refresca una vez a los 15 segundos, y la misma
  cuenta no vuelve a ejecutarlo en 60 segundos (para evitar el 429).
- Comprobado en real (HTTP puro, sin navegador en todo momento): una cuenta estática sin refrescar
  muere a los 20-30 minutos, mientras que con el centinela renovando continuamente aguanta 3 horas
  sin caerse. **Las cookies exportadas de Firefox se pueden renovar indefinidamente**; la sesión
  vinculada al dispositivo (DBSC) de las versiones nuevas de Chrome devuelve 401 al canjear, y si
  aparece 401 repetidamente hay que volver a iniciar sesión con Firefox y reexportar.

## 4.18.0

### Añadido

- **Interruptor de preferencia anónima `anon_first`** (issue #20). Desactivado por defecto. Al
  activarlo, las peticiones que no necesitan capacidad de sesión iniciada (texto puro, sin
  pensamiento, sin herramientas, sin imágenes) van como anónimas y no ocupan cuentas con cookie,
  maximizando el ahorro de cuota de cuentas; solo se elige cuenta cuando se usa 3.1 Pro / 3.8 Flash
  / pensamiento extendido / generación de imagen·música·vídeo·lienzo / envío de archivos. Se marca
  en la página de ajustes.

### Correcciones

- **La selección de cuenta por concurrencia podía elegir la misma cuenta**. La selección son dos
  pasos: "SELECT de la cuenta usada hace más tiempo + UPDATE para marcarla como recién usada", que
  antes no estaban encadenados: peticiones concurrentes podían hacer SELECT de la misma cuenta,
  cada una su UPDATE, y ambas usarla en el mismo instante, rompiendo la rotación. Se añade un lock
  que vuelve atómicos los dos pasos.

## 4.17.0

### Añadido

- **Interruptor "Continuación multi-turno en servidor" (`multi_turn`) en la página de ajustes del
  panel** (issue #27). Este interruptor de tiempo de ejecución ya existía, pero antes solo se podía
  activar escribiendo `config.json` a mano: no estaba en el panel ni leía variables de entorno, lo
  que resultaba especialmente incómodo en despliegues tipo docker/Synology. Ahora se marca
  directamente en la página de ajustes. Al activarlo, una misma cadena de conversación consecutiva
  reutiliza la misma sesión web de gemini (reconoce la continuación por el prefijo del historial;
  si acierta solo envía la última frase y deja el historial en el servidor), sin abrir una nueva
  sesión por cada mensaje enviado.

### Correcciones

- **Corregido que `multi_turn` se restableciera silenciosamente a `false` al pulsar "Guardar y
  aplicar" en el panel**. Era el único campo de tiempo de ejecución que no aparecía en el
  formulario de configuración del panel, y al guardar se hace una deserialización completa, de modo
  que los campos que el formulario no incluye se deserializan a valor cero: así que basta guardar
  una vez desde el panel para que el `multi_turn` configurado en `config.json` también se borre. Se
  incluyó en el formulario y se corrigió junto con lo anterior.

## 4.16.0

### Añadido

- **`gemini-3.8-flash` (+ `-thinking`)** (issue #26). Google ascendió el hex del antiguo 3.7 Flash
  (`56fdd199312815e2`) a 3.8 en el mismo sitio: mismo hex, y el nombre mostrado por el servidor
  pasó de `"3.7 Flash"` a `"3.8 Flash"` (confirmado por el frame de respuesta HAR de una cuenta de
  pago). **Exclusivo de cuentas de pago de Gemini** (no es despliegue gradual): las cuentas
  gratuitas siguen degradándose a 3.5 Flash-Lite. `gemini-3.7-flash` /
  `gemini-3.7-flash-thinking` se mantienen como alias (mismo hex), sin afectar a los clientes
  antiguos.
## 4.15.0

### Añadido

- **Endpoint `/v1/videos`** (issue #24). Generación asíncrona de vídeo con la forma de OpenAI(Sora):
  `POST /v1/videos` {model, prompt} crea la tarea y devuelve inmediatamente `{id, status}`;
  `GET /v1/videos/{id}` consulta el estado; `GET /v1/videos/{id}/content` descarga el MP4 al
  terminar. Por debajo reutiliza la cadena de generación de `gemini-video` (requiere cuenta Pro con
  sesión iniciada). El vídeo tarda de decenas de segundos a varios minutos, así que lo asíncrono
  encaja mejor que el `chat/completions` bloqueante. Se conserva disponible el anterior
  `/v1/chat/completions` + `model=gemini-video` (que devuelve una data URL base64).

## 4.14.0

### Añadido

- **Soporte de MySQL / PostgreSQL** (issue #22). Basta con definir la variable de entorno
  `SQL_DSN`; sin definirla sigue siendo SQLite por defecto, sin cambios necesarios.
  `SQL_DSN=mysql://user:pass@host:3306/db` o `postgres://user:pass@host:5432/db?sslmode=disable`.
  La creación de tablas se hace automáticamente y las tres bases comparten un mismo schema; las
  diferencias de dialecto (placeholders `?`/`$N`, upsert, clave primaria autoincremental, columnas
  clave TEXT, `IFNULL`/`COALESCE`) quedan todas concentradas en `dbdialect.go`, con las ~70
  consultas de la capa de negocio intactas. Verificado ejecutando cada una con mysql8 y pg16
  reales.

### Correcciones

- Se añade `X-Accel-Buffering: no` a las respuestas SSE, para evitar que el streaming pase por
  proxies inversos como nginx y se bufferee por completo en un único envío (el cliente conectado
  directamente ya recibe streaming; solo lo necesitan los despliegues que tienen un proxy inverso
  delante). Nota: la salida de una sola vez de Dify en la issue #21 no se debe a esto —en pruebas
  reales, a través del squid (ssrf_proxy) de Dify el streaming se transmite tal cual; el problema
  está del lado de Dify.

## 4.13.0

### Añadido

- **Lectura de vídeo**. El cliente puede pasar `video_url` / `input_video` (data URL) en content
  para que el modelo analice el contenido del vídeo, siguiendo la misma ruta de subida que la
  lectura de imágenes (el bit de tipo de la tupla de archivo 2 = vídeo). Requiere cookie (la
  referencia anónima es rechazada por el upstream). Probado enviando un clip de prueba rojo→verde→
  azul y preguntando cuáles eran los tres colores en orden; el modelo respondió "rojo, verde, azul"
  —efectivamente lo vio fotograma a fotograma.
- **Generación de vídeo** (`gemini-video`). `inner[49]=11` envía una tarea asíncrona y consulta
  hNvQHb para obtener el enlace de descarga de `contribution.usercontent.google.com`, recuperando
  el MP4 (data URL base64). Probado con "olas al atardecer", produjo un vídeo real de 10 segundos
  720p H.264+AAC. **Requiere cuenta Pro/de pago** (se declara como 3.7 Flash); las cuentas
  gratuitas reciben un error explícito cuando Google las rechaza según su política de contenido de
  vídeo. La generación tarda de decenas de segundos a varios minutos, así que el cliente debe
  configurar un timeout largo.
- **Eliminación automática de conversaciones** (issue #19, config `auto_delete_conversation`,
  desactivada por defecto). Tras obtener el resultado, elimina automáticamente la conversación
  dejada en gemini.google.com (rpc GzXR5e), para no acumular un montón en la cuenta. Solo tiene
  efecto con sesión iniciada; es asíncrono best-effort y no afecta a la respuesta.

## 4.12.0

### Correcciones

- **Convergencia del bucle de herramientas en clientes agénticos** (a raíz de #15). Para clientes
  como Codex, el resultado exitoso de un comando es un texto envoltorio con "Process exited with
  code 0 / Output: (vacío)"; comandos como escribir archivos o asignar valores no tienen stdout, y
  los modelos débiles (sobre todo el 3.6 Flash anónimo) interpretan "sin salida" como "no se hizo",
  así que prueban una y otra vez con otra sintaxis —en pruebas reales, la tarea "escribir hello en
  a.txt" probó 26 comandos distintos y no convergió en 90 segundos (el archivo en realidad se había
  escrito bien a la primera). Ahora el resultado de la herramienta se comprime en una señal
  clara de éxito/fracaso en una línea (✅ exit 0 e indicando que la ausencia de salida es normal, no
  reintentar; ❌ exit N), y se añade una cláusula de terminación a las instrucciones (al ver éxito,
  parar; no reintentar lo mismo). En pruebas reales la misma tarea bajó de 26-27 rondas a 2-4 y
  devolvió correctamente la respuesta final. Añadir solo la cláusula de terminación sin la señal
  clara no sirve; ambas van juntas.

## 4.11.0

### Correcciones

- **Peticiones con tools con "respuesta leída pero contestada mal"** (issue #15). Las peticiones
  enviadas por clientes agénticos (Codex, etc.) llevan en medio decenas de KB del prompt de
  desarrollador propio del cliente, que incluye frases del marco de herramientas nativo como
  "emit function calls to run terminal commands", que ahogan la instrucción de formato
  ```tool_call``` que ponemos arriba del todo; el modelo entonces responde "no tengo herramientas /
  no puedo acceder al sistema de archivos" o contesta cosas que no vienen al caso. Ahora
  `buildPrompt` vuelve a anclar el formato de herramientas después de todos los mensajes (justo
  junto a la pregunta del usuario) (con un ejemplo de comportamiento, dejando claro que el único
  canal de ejecución es ```tool_call```, que no debe escribir bloques de ```powershell para que los
  vea el usuario, y que solo se envía uno a la vez), y se relaja la regex delimitadora para tolerar
  las variantes inline (sin salto de línea) que el modelo pueda emitir. En pruebas reales, con el
  cliente real de Codex las herramientas se invocan correctamente; y con tools presentes, la
  conversación normal no se dispara por error.
- **Las respuestas compuestas solo de código ya no vuelven vacías ni provocan 502**. Al preguntar
  matemáticas / operaciones, el modelo ejecuta código directamente y toda la respuesta es solo el
  producto de la ejecución de código; tras limpiar las marcas `?code_reference/stdout` queda vacía
  y se interpreta como "frame sin contenido" devolviendo 502. Ahora, cuando tras limpiar queda
  vacía, se conservan el código y el resultado y se devuelve como bloque de código normal.

## 4.10.0

### Añadido

- **`gemini-canvas` (lienzo)**. Genera documentos HTML interactivos inmersivos. A diferencia de la
  generación de imagen/música, el producto es HTML y **va inline en la respuesta** (un bloque de
  código ```html), sin necesidad de descarga aparte. Requiere cookie; no se expone si no está
  configurado. En pruebas reales devuelve una página web interactiva completa `<!DOCTYPE html>…
  </html>` (con scripts), que el cliente puede extraer y renderizar.

## 4.9.0

### Añadido

- **Conmutación chino-inglés del panel de administración** (issue #13). Se añadió un botón de
  idioma en la barra superior, por defecto chino, que al pulsarlo cambia a inglés (se guarda en
  localStorage). Unos 190 términos cubren navegación, KPI, cabeceras de tabla, botones,
  desplegables, insignias de estado, campos y pistas de configuración en tiempo de ejecución,
  clasificación de errores, diagnóstico de conectividad, modales y tabla de despliegue. Las frases
  con interpolación de variables numéricas (como "aún se puede llamar unas N veces") se mantienen
  en chino.

## 4.8.0

### Correcciones

- **Obtener la imagen original al generar imágenes** (issue #14). La cadena plain gg-dl descargaba
  por defecto una miniatura de ~500px (512×279); añadiendo `=s0` al final de la cadena se obtiene
  el tamaño original a resolución completa (1408×768 en pruebas reales). El formato sigue siendo
  PNG.
- **Los modelos multimedia ya no pierden su producto por la ruta multi-turno**. Desde 4.5.0, al
  ampliar la condición multi-turno a las peticiones con tools, se metieron también los modelos
  multimedia (imagen/música) en la ruta multi-turno, y esta solo procesa texto, así que al activar
  `multi_turn` y luego llamar a `gemini-image` / `gemini-music` solo se devolvía un texto de
  relleno, sin imagen/música. Ahora los modelos multimedia recorren siempre la vía que recupera el
  producto, con independencia del interruptor `multi_turn`.

## 4.7.0

### Añadido

- **`gemini-3.7-flash` (incluida la versión `-thinking`)**. El frontend web de Gemini habilitó 3.7
  Flash por despliegue gradual según cuenta, y se integra. Requiere cookie configurada y **que la
  cuenta ya tenga el despliegue gradual a 3.7**; de lo contrario se degrada a 3.5 Flash-Lite (igual
  que con 3.1 Pro: sin cookie / sin despliegue gradual no se da éxito falso).

  Para el hex se usa el hex principal de la entrada 3.7 `56fdd199312815e2` (en el listado otAQ7b de
  dos cuentas independientes con el despliegue gradual aparecía ese mismo, y los usuarios con
  cuenta 3.7 que lo probaron reportaron "3.7 Flash"). No se usó el `797f3d0293f288ad` de la lista
  compat, que es el puntero genérico a "el Flash actual", y con el que un número de lote antiguo
  devuelve 3.6, haciendo pasar 3.6 por 3.7.

## 4.6.0

### Correcciones

- **Se añade el campo `index` a los `tool_calls` en streaming** (issue #10). Cada tool_call del
  delta en streaming carecía de `index`, y la especificación de streaming de OpenAI exige que el
  cliente lo use para ensamblar los tool_call fragmentados; su ausencia hacía que algunos clientes
  no pudieran ensamblarlos. Ahora se añade a cada uno en orden.

## 4.5.0

### Correcciones

- **Se completan los eventos del ciclo de vida del item en el streaming de `/v1/responses`**.
  Antes solo se enviaba `response.created` y se empujaba directamente `output_text.delta`, sin
  declarar antes el item con `response.output_item.added` + `content_part.added`. Los clientes
  estrictos de Responses (Codex, zcode, etc.) dan error a mitad al recibir una secuencia de eventos
  no conforme ("OutputTextDelta without active item" / "Turn execution failed"), mientras que la
  petición HTTP en sí devuelve 200 y el modo chat no se ve afectado, por lo que el panel parece
  normal y es difícil de localizar. Ahora se completa el ciclo de vida íntegro
  added → content_part → done tanto para el item de tipo message como para el de function_call.

### Añadido

- **Multi-turno opcional (`multi_turn`, desactivado por defecto)**. Al activarlo se usa la
  continuación en servidor nativa de Gemini por conversation_id: el cliente reenvía todo el
  historial en cada turno, y el servidor identifica la continuación mediante huella de "el
  historial menos el último mensaje"; si acierta, solo se envía el último mensaje nuevo y el
  historial se queda en los servidores de Google, evitando el muro de ~130 mil bytes por petición.
  Funciona tanto con sesión iniciada como en anónimo (en el primer turno anónimo se hace GET /app
  in situ para obtener la session cookie como soporte de la conversación, sin necesidad de cuenta).
  Los clientes agénticos con tools también recorren esta vía.

  En pruebas reales: en un escenario con dos turnos de 110KB cada uno en el que el reenvío completo
  chocaría con el muro de bytes y daría 400, la continuación solo envía el mensaje nuevo → 200, y
  las conversaciones largas ya no chocan con el muro. Ojo: el multi-turno **no amplía la ventana de
  contexto del modelo** —el contenido antiguo que excede la ventana se sigue expulsando (la ventana
  deslizante conserva lo más reciente); lo que resuelve es "conversaciones largas que no chocan con
  el muro de una sola petición + conservar el contexto reciente", no "alimentar con documentos
  ultra largos".

## 4.4.0

### Añadido

- **Generación de imagen / música**. Dos modelos nuevos, `gemini-image` (Nano Banana) y
  `gemini-music` (Lyria, ~30 segundos), que van por `/v1/chat/completions`. Los bytes del producto
  se insertan directamente como **data URL base64** en el `content` devuelto (imagen `![]`, audio
  `[]`), sin convertir a enlace externo —un enlace externo requiere cookie para descargarse y el
  cliente no la tiene. Ambos requieren sesión iniciada y no aparecen en `/v1/models` si no hay
  cookie configurada.

  La recuperación del producto se atascó mucho tiempo en un 403 de descarga; la causa raíz son tres
  cosas (confirmadas con captura de tráfico + pruebas reales): el host de descarga solo reconoce
  las 18 cookies del dominio `.google.com` (pasar la cadena entera incluyendo las específicas del
  host accounts provoca 403); la cadena de imagen cruza de dominio con 302, y el cliente por
  defecto no lleva la Cookie al nuevo dominio (se resuelve siguiéndola manualmente y reenviándola
  en cada salto); y la respuesta de imagen devuelve la cadena plain `gg-dl`, donde GET solo
  devuelve un puntero text/plain, y la imagen real requiere cambiar al prefijo `rd-gg-dl`.

  base64 no cuenta para los output token —una imagen tiene millones de caracteres, y contarla
  equivaldría a hacer que el usuario pague por binario.

  En pruebas reales: `gemini-image` → PNG de 512×279 (106KB, decodificable); `gemini-music` →
  MP3 válido de 744KB.

## 4.3.0

### Añadido

- **Servidor MCP (web_search)**. Además de la interfaz OpenAI, ahora el mismo proceso y el mismo
  puerto también montan un servidor MCP en `/mcp`, que expone la búsqueda en internet del frontend
  web de Gemini como una herramienta `web_search`, permitiendo a clientes MCP como Claude Desktop /
  Claude Code / Cursor "buscar en internet con Gemini", devolviendo una respuesta sintetizada +
  enlaces a las fuentes.

  El transporte es HTTP (Streamable HTTP); una vez desplegado como servidor, los clientes remotos
  solo necesitan conectarse a la URL, reutilizando el pool de cuentas / pool de proxies / rate
  limiting del backend; se puede buscar en anónimo, sin necesidad de cookie. Se autentica con la
  misma API key (`Authorization: Bearer <key>`). JSON-RPC 2.0 escrito a mano, sin dependencias de
  SDK de terceros.

  La configuración del cliente está en la sección "MCP" del README. `web_search(query)` devuelve la
  respuesta, con la lista de fuentes `Sources:` al final.

- **Análisis de las fuentes de la búsqueda en internet**. Antes solo se devolvía el texto de la
  respuesta, perdiendo las fuentes de grounding; ahora se extraen de los frames de respuesta la url
  / título / resumen de cada fuente, eliminando el ancla de fragmento `#:~:text=` del final de la
  URL. Actualmente se usa en el `web_search` del MCP; la respuesta de la interfaz OpenAI no se ve
  afectada.

## 4.2.0

### Añadido

- **Pensamiento extendido: los tres modelos pueden activarlo**. Se añaden
  `gemini-3.6-flash-thinking` / `gemini-3.5-flash-lite-thinking` / `gemini-3.1-pro-thinking`; el
  nombre de modelo reportado por el servidor lleva `Extended`, y la cadena de pensamiento medida en
  real dio 2467 / 1059 / 583 caracteres (frente a 0 / 0 / 268 en las versiones normales). Igual que
  con 3.1 Pro, solo se expone cuando hay cookie configurada —las peticiones anónimas con este
  interruptor son ignoradas silenciosamente por el servidor (reprobado con el header completo, no
  es que lo enviemos mal).

  Es el interruptor "pensamiento extendido" de la UI web, ortogonal al modelo, no tres modelos
  adicionales.

  **El interruptor está en el header del modelo, no en el payload.** Antes solo enviábamos la forma
  mínima de 5 elementos `[1,null,null,null,"<hex>"]`, mientras que el navegador envía 18 elementos,
  con el índice 14 = MODE del modelo y el índice 15 = bit de pensamiento. Rellenar solo el
  `inner[80]=2` del payload dio en pruebas reales cadenas de pensamiento de 0 caracteres en los
  tres modelos; solo surte efecto al completar el header —la misma regla que en la propia selección
  de modelo, donde "el header pesa más que inner[79]".

  Complementos: `inner` se amplió de 80 a 97 ranuras (el navegador usa 97-98 ranuras, y `inner[80]`
  ni siquiera tiene posición en un array de longitud 80); se añaden `x-goog-ext-525005358-jspb` y
  los de valor fijo `x-goog-ext-73010989-jspb` / `-73010990-jspb`; se añaden `inner[91]` /
  `inner[96]`. Tras completarlo, la comparación ranura a ranura con el navegador solo difiere en
  `[3,4]` —el token de botguard y su nonce emparejado—, que el servidor no reconoce si los
  calculamos nosotros y por eso se omiten a propósito.

### Correcciones

- **Desactivación automática de cuentas muertas**: tras 3 fallos de autenticación consecutivos
  (solo cuentan 401/403) se cambia automáticamente a disabled. Antes `fail_count` solo se acumulaba
  y no se procesaba, y la selección de cuenta es `ORDER BY fail_count ASC` —las cuentas malas
  quedan al final, pero cuando en el pool solo quedan cuentas malas se siguen seleccionando, así
  que cada petición tenía que pagar primero un round-trip XSRF por ella antes de dar error.
  Desactivar no elimina: la cookie son datos importados por el usuario, el juicio puede equivocarse,
  y se deja para que el usuario decida.
- **Validar la identidad antes de refrescar la cookie**: si tras fusionar el Set-Cookie cambian
  `SAPISID` o `__Secure-1PSID`, se descarta todo sin escribir. Sin esta validación, si el upstream
  cambiara todo el conjunto de sesión en la respuesta, las credenciales de la cuenta A se
  escribirían silenciosamente en la fila de la cuenta B —el panel seguiría mostrando la etiqueta
  original, pero lo que se envía de verdad es la sesión de otra.
- **Rescatar la cookie una vez antes de darla por inválida**: cuando no se obtiene el XSRF token,
  primero se fuerza una rotación y se reintenta; solo si no se puede rescatar se pasa a la
  siguiente cuenta. Antes se cambiaba de cuenta directamente, abandonando una cuenta que
  posiblemente solo tenía la cookie caducada.

## 4.1.1

### Correcciones

- **El panel no abre al hacer proxy inverso a un subdirectorio** (por ejemplo
  `example.com/gemini/admin`). En `index.html` las 30 direcciones eran absolutas, el navegador las
  resolvía contra la raíz del sitio y todas caían en `example.com/admin/api/…`, perdiéndose el
  prefijo. Ahora son direcciones relativas.

  De paso, aclarar una trampa: **no basta con cambiar a direcciones relativas**. Las direcciones
  relativas se resuelven contra el **directorio** de la URL del documento, y `/admin` y `/admin/`
  difieren en un nivel de directorio —`api/stats` bajo `/gemini/admin/` se resuelve como
  `/gemini/admin/api/stats` (correcto), pero bajo `/gemini/admin` se resuelve como
  `/gemini/api/stats` (incorrecto). Por eso el servidor redirige con 301 de `/admin` a `admin/`,
  unificando la URL del documento con barra final. El Location usa un **valor relativo** —si se
  escribiera `/admin/` el navegador saltaría a la raíz del sitio y volvería a perderse el prefijo.

  El proxy inverso no necesita ninguna configuración adicional: basta reenviar `/prefijo/` al `/`
  de este servicio.

## 4.1.0

### ⚠️ Cambios incompatibles

- **El proxy y la cookie pasan a tener una sola entrada cada uno**: el proxy solo se reconoce en la
  página "Pool de proxies", y la cookie solo en la página "Pool de cookies". El campo de entrada
  "proxy estático" de la página "Ajustes", la entrada de cookie individual de la tarjeta de cookie
  y el endpoint `/admin/api/cookie` se han eliminado.

  Por qué no es "una opción simple más" sino que hay que eliminarlos: estas dos rutas singleton
  recorren la rama "ID=0", así que comparten el mismo slot de rate limiting que la conexión
  directa, y la función de escritura de salud empieza con `id<=0 return` y se salta todo. El
  resultado es fail_count siempre 0, last_ok_at siempre vacío, sin rotación, sin enfriamiento por
  fusible, y en el panel ni siquiera se distingue si el tráfico fue por proxy o directo (en pruebas
  reales: antes de migrar, las peticiones por proxy estático quedaban registradas en la tabla
  requests como `proxy_id=0`, indistinguibles de una conexión realmente directa). Poner uno en el
  pool y ser estricto en todas partes es mejor.

  **Al actualizar se migra automáticamente, sin tocar tus valores originales**: el proxy estático y
  la cookie individual guardados en kv se trasladan al pool correspondiente en el primer arranque,
  dejando los valores originales intactos (así que hacer rollback a 4.0.0 sigue funcionando). La
  migración solo marca "migrado" **tras entrar con éxito en el pool**; si falla, se deja para
  reintentar en el siguiente arranque —4.0.0 no valida nada del campo de proxy estático, y lo
  guardado podría ser un `1.2.3.4:8080` sin scheme; borrar el valor antes de meterlo en el pool lo
  haría desaparecer de la nada.

- **`--proxy` / `--cookie-file` pasan a seguimiento declarativo**: al cambiar el valor se
  reemplaza la entrada correspondiente del pool, en vez de añadir otra. Cuando el valor no cambia
  no se toca el pool en absoluto, y lo que hagas en el panel (añadir/eliminar/editar/desactivar)
  manda. Los despliegues que rotan `cookie.txt` deben prestar especial atención a este cambio: al
  deduplicar por valor se irían acumulando cookies muertas en el pool una y otra vez, y seguirían
  enabled y participando en la rotación.

- **`--proxy` / `--cookie-file` / `config.json` se degradan a parámetros de siembra**. Ya no son
  una "segunda configuración"; solo importan sus valores al pool al arrancar (deduplicando por URL
  / contenido de cookie), y a partir de ahí manda siempre el pool. Los cambios surten efecto al
  reiniciar. Antes la semántica de `--proxy` era "reserva cuando el pool de proxies está vacío", y
  **en cuanto guardabas una vez los ajustes en el panel, kv ya tenía el registro completo y
  `--proxy` dejaba de tener efecto para siempre** —cambiabas el compose y no servía de nada. Ahora
  ya no existe esa trampa.

### Añadido

- **Las peticiones ultra largas ya no hacen que el upstream se coma la frase que acabas de
  preguntar**. El upstream pone un tope de unos **130 mil bytes UTF-8** por petición; al superarlo
  **trunca silenciosamente desde la cola** y no da error; y como nosotros montamos el prompt en
  orden, con el mensaje más reciente al final, lo truncado es justo lo que acaba de preguntar el
  usuario, de modo que el modelo solo ve el preámbulo del sistema de delante y devuelve un saludo
  genérico, sin responder ni invocar herramientas. Todo esto parece "el modelo se ha vuelto tonto"
  o "el soporte de herramientas va mal".

  Ahora hay dos casos:

  - **Con cookie configurada**: la conversación ultra larga se convierte automáticamente en un
    adjunto `message.txt` que se sube, y en el cuerpo de la petición solo queda una instrucción
    corta, sorteando así el muro. **Pero los adjuntos también tienen límite**: el contenido que el
    modelo puede ver suma unos 160 mil bytes; lo que lo exceda se sube pero no se puede leer. Así
    que esta vía sube la longitud disponible de 130 mil a unos 160 mil, una mejora limitada; las
    conversaciones realmente largas siguen necesitando que el cliente las comprima por su cuenta.
    (Si falla la subida **no se vuelve silenciosamente a inline**: así el upstream truncaría la
    pregunta más reciente y el cliente recibiría un 200 que no responde a lo preguntado sin poder
    ver el problema; es preferible dar un error explícito.)
  - **Sin cookie**: se devuelve directamente 400 + `context_length_exceeded`, y el mensaje de error
    sugiere que importar cookie permite obtener un límite mayor. En anónimo se puede subir el
    archivo, pero al referenciarlo dentro de la conversación el servidor lo rechaza, así que en
    anónimo no hay otra solución.

  **No descartamos el historial por nuestra cuenta**: eso seguiría siendo perder datos
  silenciosamente, solo que en otro sitio; el cliente cree que se envió todo el tramo y el modelo
  ha olvidado cosas. `context_length_exceeded` es una señal que los clientes compatibles con OpenAI
  reconocen, y los clientes agénticos que la reciben comprimen su propio contexto y reintentan, mucho
  más listo que si nosotros descartáramos unos cuantos tramos a ciegas.

  El límite `max_prompt_bytes` es 128000 por defecto, modificable en el panel; 0 = desactiva la
  comprobación.
  **La unidad son bytes UTF-8, no tokens**: en pruebas reales el muro del upstream va por bytes y es
  independiente del idioma —alineando chino e inglés al mismo número de bytes y lanzando 3 pruebas
  cada uno, unos 129,950 bytes pasaron 3/3 cada uno, y 135,990 bytes 1/3 cada uno, mientras que el
  número de tokens del mismo lote de peticiones difería en 1.9 veces. Si se fijara el umbral por
  tokens, el mismo número sería demasiado permisivo para inglés y se quedaría atascado en torno a un
  tercio de la capacidad real para chino.

- **Lectura de imágenes (con cookie configurada)**: el `image_url` de `/v1/chat/completions` y el
  `input_image` de `/v1/responses` ahora sí se pasan de verdad al modelo. Admiten URL `data:` y
  enlaces http(s) (estos últimos se descargan primero y luego se suben —pasar el enlace
  directamente al upstream no sirve, porque solo reconoce adjuntos de su propio almacenamiento—),
  con un tope de 12MB por imagen. Las imágenes remotas salen por la misma salida que la
  conversación, ya que si no equivaldría a exponer una IP más. En anónimo no se admite, por la
  misma razón.

- **Renovación automática de cookies**. El upstream, en casi cada respuesta, refresca `SIDCC` /
  `__Secure-1PSIDCC` / `__Secure-3PSIDCC` con `Set-Cookie`, y el navegador, al recibirlo, lo
  mantiene vivo siempre; nosotros antes solo enviábamos, sin recibir, así que la copia guardada se
  quedaba con el valor antiguo de cuando se importó. Ahora cada petición fusiona de vuelta en la
  cuenta las entradas refrescadas, igual que hace el navegador.

  **Pero esto no logró prolongar la vida de las cuentas**: cookies del mismo origen, con renovación
  + keepalive duraron 86 minutos, y sin ninguna de las dos duraron 114 minutos (n=3, y todas
  compartían la misma sesión que el navegador real, muy probablemente porque ambos lados rotaban
  SIDCC y chocaban). Así que esto solo declara "es coherente con el comportamiento del navegador",
  no declara "las cuentas viven más" —por qué la cuenta muere en una o dos horas sigue sin
  esclarecerse.

- **Keepalive de sesión**. El navegador llama cada 10 minutos a
  `accounts.google.com/RotateCookies`, con el intervalo especificado por el servidor en la página
  (los 600 segundos del cuerpo `[["identity.hfcr",600],…]`). Esta llamada **también** devuelve
  Set-Cookie, y refresca el mismo grupo de tres entradas que el punto anterior (9 de 12 veces en
  una captura, 14 de 15 en otra), así que también hay que fusionarla de vuelta en la cuenta. En toda
  la captura ninguna respuesta reestableció `__Secure-1PSIDTS`. Un fallo **no cuenta para la salud
  de la cookie**: llama a `accounts.google.com`, y que la conversación funcione o no es otra cosa;
  marcar la cuenta como mala por un parpadeo de red la hundiría en la selección de cuenta y
  perjudicaría la disponibilidad.

- **Botón "Detectar" en el pool de cookies**: pulsándolo se sabe al momento si la cuenta sirve o
  no, sin tener que probar con una conversación real. Distingue "sesión válida" de
  "caducada/inválida" —esta última el upstream no la rechaza, solo te trata como anónimo, así que
  mirando solo si la petición tuvo éxito no se distingue.

- **El modal de importar Cookie pasa a tener dos formas de rellenarlo**, por defecto "pegar la
  cadena entera": basta pegar el `name=value; name=value; …` copiado del navegador, que además es
  lo más parecido a lo que envía el navegador real. Si se quiere rellenar entrada por entrada, se
  cambia a la otra pestaña.

- **Al invalidarse una cookie se pasa a la siguiente cuenta**, en vez de sacrificar toda la
  petición. Antes, si la cuenta elegida fallaba, esta petición fallaba directamente y las demás
  cuentas buenas del pool no tenían ninguna oportunidad —cuanto más grande el pool, más fácil
  pisar una mina.

- **Cada cuenta de cookie se ancla a su propia salida**. Antes el pool de cookies y el pool de
  proxies rotaban de forma independiente, y una misma cuenta de Google enviaba peticiones desde
  decenas de IP distintas, lo que a ojos de Google es un rasgo típico de cuenta compartida. Ahora
  la cuenta, la primera vez que usa una salida, se vincula a ella y a partir de ahí siempre va por
  esa, cambiando solo si la salida deja de estar disponible.

- **El registro de peticiones indica qué cuenta de cookie se usó** (también en los registros de
  fallos), de modo que en el panel se ve directamente qué cuenta está dando problemas, sin tener
  que adivinarlo por la hora. Las que no llevan cookie muestran "anónimo".

- **El streaming real completa las dos últimas vías**: `/v1/responses` ahora empuja
  `response.output_text.delta`, y `/v1/chat/completions` con `tools` también envía a medida que
  produce. Antes ambas recibían todo y luego enviaban, porque el delimitador
  ```` ```tool_call ```` necesita el texto completo para parsearse. Ahora el cuerpo pasa por una
  compuerta incremental que solo deja salir la parte **que con certeza no está dentro del
  delimitador**: la cola con medio delimitador de apertura (`` `` `` o `` ```tool_c ``) también se
  retiene a la espera del siguiente frame, porque enviarlo antes y descubrir luego que era el
  delimitador ya sería tarde. En pruebas reales, el resultado en streaming y sin streaming es
  idéntico carácter a carácter.
- **El fusible de proxy pasa a régimen de enfriamiento**: tras fundirse por 5 fallos consecutivos,
  por defecto descansa 120 minutos y vuelve automáticamente al pool; la duración se modifica en la
  página "Ajustes", 0 = volver al antiguo destierro permanente. El 120 sale de pruebas reales: las
  salidas bloqueadas por Google se recuperan solas a los 106-121 minutos. Antes, fundirse era
  desterrar para siempre, con lo que nunca llegaba una vez de éxito que pusiera el contador a cero,
  y solo se podía restablecer a mano.
- **Dos interruptores de degradación, ambos desactivados por defecto** (se mantiene el
  comportamiento antiguo; activarlos es una elección explícita):
  - `fallback_direct`: cuando no sirve ninguna salida del pool de proxies, se recurre a la conexión
    directa. Desactivado devuelve 429, y la IP local no se expone al upstream; activado escribe un
    log indicando que esta vez fue directa.
  - `fallback_anon`: cuando la cookie se invalida, se degrada a anónimo y se sigue ejecutando.
    Desactivado da error directamente —tras invalidarse la cookie el upstream no la rechaza, solo te
    trata como usuario anónimo, `3.1 Pro` se degrada silenciosamente a `3.5 Flash-Lite`, la cadena
    de pensamiento desaparece, y el cliente no lo nota en absoluto, así que por defecto se prefiere
    fallar explícitamente.
- **El número de versión de `bl` sigue automáticamente al upstream**: cada 6 horas se captura una
  vez desde la página `/app`; si la forma no coincide o no se captura, se sigue usando el valor
  fijado en la configuración. Se puede desactivar en la página "Ajustes".

### Correcciones

- **El contenedor no arranca y da `unable to open database file (14)`**. La imagen se ejecuta como
  nonroot (uid 65532), y el bind mount sobrescribe el propietario de `/data` de la imagen con el
  propietario del directorio del host; si el directorio no existe en el host, Docker lo crea
  automáticamente con propietario root, y entonces no se puede escribir. En compose se cambió por
  defecto a un volumen con nombre, y el bind mount se conserva comentado indicando que primero hay
  que ejecutar `sudo chown -R 65532:65532 ./data`. El mensaje de error también se cambió para
  imprimir juntos el uid actual, el directorio y las dos soluciones —14 es SQLITE_CANTOPEN, y por el
  texto literal nadie pensaría en permisos.

- **Se define `HTTP_PROXY` / `ALL_PROXY` pero no surte efecto y no avisa nada**. Este programa
  deliberadamente no lee esas variables de entorno (un export suelto en el host cambiaría
  silenciosamente la IP de salida mientras el panel sigue mostrando conexión directa), pero "no
  leerlas" y "no decir nada" son dos cosas distintas. Ahora, al arrancar, si se detectan y el pool
  de proxies está vacío, se imprime una línea explicándolo.

- **De vez en cuando se saltaba el proxy y se iba directo**. El `Scan` de `loadProxies` fallaba y
  hacía `continue`, `rows.Err()` no se comprobaba en absoluto, y una interrupción del recorrido se
  tomaba como lectura completa, de modo que la lista de proxies quedaba sobrescrita por un
  resultado a medias o incluso vacío; y "¿hay pool de proxies configurado?" se decide precisamente
  por la longitud de esta lista, así que al quedar vacía se volvía a la conexión directa, exponiendo
  la IP real del desplegador al upstream, con solo unas pocas peticiones normales en el log. La
  condición desencadenante es que al terminar cada petición se relee entera la tabla de proxies, y
  choca con el UPDATE que acaba de emitirse en el WAL con SQLITE_BUSY. Ahora, si la lectura falla,
  se conserva el último pool, y la escritura de vuelta del resultado solo modifica esa entrada en
  memoria, sin releer la tabla entera.
- **Las respuestas vacías no se reintentan**. El upstream ocasionalmente devuelve HTTP 200 pero sin
  ningún frame de contenido (rechazo instantáneo); el código antiguo solo reintentaba en caso de no
  200 o error de red, así que un parpadeo que podría autocorregirse se convertía directamente en un
  502 visible para el cliente. El criterio es **si hay frame de contenido**, y no se puede usar
  `BardErrorInfo`—el frame de fin de una respuesta normal también lleva código de error.
- **`inner[41]` no coincide con el navegador**: nosotros enviamos `[2]`, y las capturas del
  navegador en tres escenarios dan todas `[1]`. Este valor se copió en su momento y a nivel de
  protocolo ya ha sido refutado.

### Correcciones de documentación

- **"Bajar el ritmo no sirve" es falso**. El criterio antiguo era que en una salida residencial el
  modo racha 103-177 y el modo lento 81-166 casi se solapaban por completo. La observación era
  correcta, pero la atribución estaba mal: la salida residencial se degrada sola tras usarse mucho
  (8 salidas previamente filtradas como limpias corriendo en modo lento, 6 de ellas con tasa de
  fallo superior al 40% a mitad de camino), y la varianza de la degradación superaba la del ritmo.
  Tras cambiar a IP estática para eliminar este factor de confusión: la misma IP, en racha, fue
  bloqueada al llegar a **188 veces**; cambiando a **10 veces/minuto durante 800 llamadas seguidas
  a lo largo de 110 minutos, no fue bloqueada ni una vez**. Así que `per_ip_rph=80` es el límite
  inferior conservador del modo racha, y a baja velocidad se puede subir bastante.
- **La duración de recuperación tras ser bloqueado** pasó de "no medida" a medida en real:
  **106-121 minutos**.
- Todos los "unas 85 veces" residuales en el panel y los comentarios de código se sustituyen por
  el intervalo medido.

## 4.0.0

### ⚠️ Cambios incompatibles

- **Se eliminan 5 alias de modelos antiguos**: `gemini-3.5-flash`,
  `gemini-3.5-flash-thinking`, `gemini-3.5-flash-thinking-lite`, `gemini-auto`,
  `gemini-flash-lite` devuelven **400** si se pasan. No tienen entrada correspondiente en el
  servidor, y todos caían en el mismo backend, así que mantenerlos solo hacía creer que había cinco
  modelos entre los que elegir. Ahora solo se exponen los tres que existen realmente en la lista del
  servidor: `gemini-3.6-flash`, `gemini-3.5-flash-lite`, `gemini-3.1-pro`.
  **Antes de actualizar, comprueba el nombre de modelo configurado en tu cliente.**

La base de datos no requiere migración; las sentencias de creación de tablas son todas
`IF NOT EXISTS`, y las bases antiguas sirven directamente.

### Añadido

- **Pool de cookies**: se pueden importar varias cuentas de Google con sesión iniciada; las
  peticiones rotan automáticamente dando prioridad a la usada hace más tiempo, y solo cuando el
  pool está vacío se recurre a la cookie individual de la página "Ajustes". La lista solo muestra
  un resumen censurado (número de cookies / si están completas las entradas clave / últimos 4
  dígitos de SAPISID / número de fallos); la cookie completa no sale a la red.
- **Cadena de pensamiento (`reasoning_content`)**: el proceso de razonamiento de `gemini-3.1-pro`
  antes de cada respuesta ahora se expone. En no streaming se pone en
  `message.reasoning_content`; en streaming se empuja como `delta.reasoning_content`, y solo se
  empieza a empujar el cuerpo cuando se han terminado de enviar todos los bloques de pensamiento
  (en el mismo orden que el upstream). Los tokens se listan aparte en `usage.reasoning_tokens` y
  **no cuentan para `completion_tokens`** —el cliente los pliega por defecto sin mostrarlos, y
  contarlos equivaldría a hacer que el usuario pague por una salida que no ve.
- **Renovación del panel de administración**: se adopta el tema claro del lenguaje de diseño de
  Gemini, las listas tienen altura fija que llena el viewport + paginación, los errores se
  clasifican según "qué hay que hacer", y el protagonista de la primera pantalla pasa a ser la
  "distancia a la línea roja de bloqueo".
- **Imagen de contenedor multi-arquitectura**: al hacer push a main o al crear un tag, se compila y
  se publica automáticamente en ghcr.

### Correcciones

- **Configurar una cookie válida provocaba 400 en todas las peticiones**. Las peticiones con cookie
  deben llevar además el XSRF token (campo de formulario `at`), y nosotros solo enviábamos `f.req`.
  Las peticiones anónimas no piden este campo, por lo que este defecto no se había expuesto en
  mucho tiempo —basta con poner una cookie **válida** para que la funcionalidad de cookie sea 100%
  inutilizable; y poner una cookie **caducada** en cambio "funcionaba" (se trataba como anónimo),
  tapando justo el problema. Ahora se obtiene automáticamente el token de la página de Gemini, se
  cachea por cookie, y se reobtiene automáticamente al caducar.
- **La salud del pool de cookies nunca se escribía de vuelta**. `markAccountResult` era código
  muerto, `fail_count` siempre 0, `last_ok_at` siempre vacío, y el panel mostraba esas dos columnas
  —equivalía a dar a operaciones un informe falso de "todo sano siempre". Ahora al terminar la
  petición se escribe de vuelta, y **solo se cuenta 401/403 como fallo de la cookie**: errores de
  red, fallos de proxy y bloqueos de Google (302) no cuentan, porque de lo contrario la alta tasa
  de fallo de los proxies residenciales convertiría ese número en ruido del proxy, y una cookie
  buena quedaría registrada como la que más falla.
- Varios defectos de visualización del panel de administración: el gráfico de tendencia crecía sin
  límite, la tarjeta de login se estiraba al máximo de alto, los botones se desbordaban de la
  tarjeta, y pulsar la página actual rerenderizaba la página entera.
- La página de ajustes se comprimió de 2234px a 1075px: los 12 elementos de configuración se
  dividen por semántica en cuatro grupos, con columnas adaptativas dentro de cada grupo.

### Correcciones de documentación (todas conclusiones antiguas refutadas por pruebas reales)

- **`gemini-3.1-pro` con una cookie válida sí funciona de verdad**. La documentación antigua decía
  "una cuenta gratuita, incluso con sesión iniciada, solo obtiene 3.6 Flash" —esa observación se
  obtuvo en condiciones de falta del XSRF token, cuando las peticiones con cookie ni siquiera
  podían salir. Tras completarlo, 6 llamadas seguidas devolvieron todas `3.1 Pro`, y cada vez con
  cadena de pensamiento.
- **El límite por IP única** pasa de "unas 85 veces" al intervalo medido **80–180 veces**, y se
  aclara que lo determina principalmente la **estrategia de conexión y la calidad de la salida**,
  no la velocidad de las peticiones: con la misma concurrencia de 10, reutilizando el pool de
  conexiones se llega a 172/177, y creando una conexión nueva cada vez solo 106/109.
- **Al pasar por proxy, lo que Google ve es nuestro propio fingerprint TLS**, no el del nodo de
  salida (JA3 medido: conexión directa con stdlib y a través de proxy tienen el mismo fingerprint;
  bajo el mismo proxy, cambiando a tls-client, difiere). El comentario del código estaba al revés.
  Pero en pruebas reales esto no afecta al umbral de bloqueo, así que no se cambió la
  implementación por ello.

## 3.0.0

Proxy inverso compatible con OpenAI en binario único + panel de administración en chino.
