# DiOffice Asset Guide v0.1

## Goal

Use free/legal starter assets to validate the product quickly while keeping a clean path toward original DiOffice art.

## Art direction

- Chibi pixel software office
- Cozy RPG-like office atmosphere
- Original DiOffice identity over time
- Do not copy proprietary MapleStory characters, maps, sprites, or UI

## Logical grid

Primary logical tile grid: 16x16 px.

Use integer display scaling where possible.

## Starter asset strategy

### Core office pack

2D Pig Pixel Office

Use for prototype office environment, furniture, computers, and visual references.

License baseline recorded for starter bundle: CC0/public domain.

### Supplementary CC0 assets

Only include third-party assets in the repository when redistribution terms are clear.

Assets that are free-to-use but unclear about redistribution should remain source links only until license verification is complete.

## Required repository structure

assets/
- licenses/
- raw/
- processed/
- characters/
- furniture/
- rooms/
- effects/
- manifests/

## Asset provenance

Every externally sourced asset should record:
- asset name
- source URL
- creator
- license
- source filename
- local filename
- modification notes
- checksum where useful

## Asset manifest contract

Game logic should refer to stable asset IDs, not hard-coded source filenames.

Example conceptual record:

```json
{
  "id": "office-desk-basic-01",
  "type": "desk",
  "gridWidth": 2,
  "gridHeight": 2,
  "source": "2dpig-pixel-office",
  "sprite": "processed/furniture/desk-basic-01.png"
}
```

## Character contract

Each permanent employee eventually needs a consistent sprite definition:
- idle
- walk directions
- seated
- typing
- thinking
- talking
- success

MVP can use a simplified set while keeping the manifest future-compatible.

## Monitor states

At minimum:
- off
- terminal
- code
- browser
- testing
- warning/approval

The pixel monitor is a status visualization. Detailed terminal/code/browser content belongs to the React Workstation UI.

## Asset generation later

Frontend/design employees may eventually create or request assets through an Asset Service.

Generated assets must pass:
- size/grid validation
- transparency validation
- style consistency check
- manifest generation
- provenance metadata

## Production direction

Free assets are for MVP acceleration. Deni, Nabil, Raka, signature office props, and major rooms should eventually become original DiOffice IP.
