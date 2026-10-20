# "Ver placa" (PanelView): configuración y runbook del gate en vivo

Guía para activar el botón **Ver placa** de Home Assistant en el device y para
ejecutar el gate en vivo de la v0.29.0.

> **Estado (v0.29.3, gate en vivo 2026-10-16):** el ojo enciende la placa
> (`DEVADDR=20`), dura los 30 s del auto-off, **tres ojos seguidos funcionan** y la
> **imagen llega a la tarjeta de HA** (visto en un móvil): en reposo la tarjeta
> muestra negro (fondo H.264 del bridge, go2rtc permanece conectado) y al pulsar
> "Ver placa" entra la calle sin el retraso de hasta 90 s de antes. Pendiente
> validar OÍR/HABLAR con alguien en la calle y la cesión a un timbre real.
>
> **Diagnóstico:** si el ojo cuelga, buscar en el log del bridge
> `panelview: answer SDP del ojo inválido` o `SIP: respuesta ajena descartada`, y
> en `/tmp/linphone.log` del device si el `200 OK` del `bt_vct` se retransmite
> (señal de que no le llegó nuestro ACK). Un cierre por el nativo se ve como
> `SIP: el remoto colgó el diálogo` + `panelview: ojo cerrado (remote_bye)`.
> Si la tarjeta no pinta: `/tmp/go2rtc.log` no debe mostrar `ERR [exec] timeout`
> (el RTSP del bridge nunca va mudo desde la v0.29.3) y `GET :1984/api/streams`
> debe listar al navegador como consumer del stream de la tarjeta.

## 1. Configuración en el device

El deploy (`deploy-standard.sh`) sube el binario y `VERSION`, pero **no** el
`config.yaml`. Hay que añadir el bloque a mano en el device. Es fail-closed: sin
`enabled: true` y sin `devaddr` no se dispara nada.

```yaml
streaming:
  panelview:
    enabled: true
    devaddr: "<capturado>"     # dirección OWN de la placa de calle (paso 0 del gate)
    timeout_seconds: 30        # auto-off del ojo
    caller_user: "webrtc"      # reservado; aún no cableado (la identidad From la fija la pata webrtc del cliente)
    send_who8_arm: false       # no enviar *8*2#5#4*16## tras el ACK
```

Para **HABLAR** (micro de HA hacia la placa) hace falta además el bloque
`sip.answerer` activo en el mismo config. Es una sección distinta de
`streaming.panelview`: el TalkSender que usa PanelView se instancia anidado bajo
el answerer. VER y OÍR no dependen de ello.

```yaml
sip:
  answerer:
    enabled: true              # requerido: TalkSender se construye bajo el answerer
    talk_enabled: true         # requerido para HABLAR (subida de voz a la placa)
```

El `devaddr` es un valor OWN (por ejemplo `"11"`). Se obtiene del paso 0, no se
inventa.

Tras editar el config, reiniciar el bridge para que lo lea.

## 2. Gate en vivo (una sola sesión)

Ejecutar tras el deploy único. Cada paso depende del anterior.

### Paso 0: capturar el DEVADDR de un timbrazo real

1. Con el monitor de la placa, pulsar el timbre desde la calle (o pedir a alguien
   que lo haga) y dejar que llegue el INVITE al bridge.
2. Leer el `a=DEVADDR` del INVITE entrante en `/tmp/linphone.log` (log del
   softphone que recibe el timbrazo; confirmar en qué máquina se escribe antes
   de buscar):

   ```bash
   grep -i "DEVADDR" /tmp/linphone.log
   ```

3. Copiar ese valor a `streaming.panelview.devaddr` del config del device y
   reiniciar el bridge.

### Paso 1: sonda de enrutado

Objetivo: confirmar que el INVITE del ojo llega al `bt_vct` nativo y que el
c300x no se reinicia.

1. Anotar el uptime antes de empezar: `ssh bticino 'uptime'`.
2. Pulsar **Ver placa** en HA (o publicar `panelview.request`).
3. Comprobar, en este orden:
   - (a) El bridge **no** se auto-contesta al INVITE propio (correlación de
     Call-ID).
   - (b) `bt_vct` recibe el INVITE (revisar el log del bridge y el de `bt_vct`).
   - (c) La **placa de calle se enciende**.
   - (d) El uptime **se mantiene**. Comparar con el valor anotado. Si hay reboot,
     parar el gate.
4. Si `bt_vct` no es alcanzable como `c300x`, aplicar el fallback del spec
   (des-registro temporal o contacto directo) y repetir el paso 1. No seguir con
   los pasos siguientes hasta que el enrutado funcione.

### Paso 2: VER

- Con el ojo activo, el vídeo de la placa aparece en la tarjeta de HA.
- Anotar la latencia y si hay cortes.

### Paso 3: OÍR

- Alguien habla junto a la placa. ¿Se oye en HA?
- Validar con captura del sink y medida de RMS (es el gap histórico de audio de
  bajada).
- Nota (v0.29.3): el OÍR del ojo entra por la entrada prioritaria del relay de
  audio (`127.0.0.1:10012`) y desplaza al tap de `lo:5000`; si no llega audio, el
  relay rellena con silencio speex PT110. **No desactivar** `live_audio_on_call`
  para "aislar" OÍR: antes de la v0.29.3 eso dejaba la pista de audio vacía y la
  tarjeta HA sin imagen (el ffmpeg de go2rtc no emite nada sin audio).

### Paso 4: HABLAR

- Hablar por el micro de HA. ¿Se oye por el altavoz de la placa?
- **Vigilar el uptime durante todo el paso.** Si el c300x se reinicia, es un
  fallo de no-interferencia: parar y hacer rollback (sección 3).

### Paso 5: auto-off y cesión al timbre

- Dejar el ojo activo sin acción. A los ~30 s (`timeout_seconds`) el sensor
  **Placa en vista** debe pasar a `idle` y el bridge debe enviar BYE.
- Re-disparar **Ver placa** tras el auto-off: debe funcionar de nuevo.
- Con el ojo activo, pulsar el timbre desde la calle: el timbre real debe tener
  prioridad y el ojo debe ceder.

## 3. Rollback

Dos niveles, del más ligero al más pesado.

**Desactivar la funcionalidad (sin cambiar binario):**

```yaml
streaming:
  panelview:
    enabled: false
```

Después reiniciar el bridge. Con `enabled: false` el subsistema no se instancia.

**Volver al binario anterior:**

```bash
./deploy-standard.sh rollback   # restaura el binario .prev del device
```

Tras cualquiera de los dos, comprobar el uptime y que el bridge arranca sano con
`./deploy-standard.sh status`.
