---
name: tambo
description: The waystation for your models — a warm, angular operator panel for llama.cpp.
colors:
  primary: "#e2622c"
  primary-hover: "#ea7141"
  primary-press: "#d4571f"
  primary-ink: "#f0854f"
  primary-on: "#1f0e05"
  ground: "#14100c"
  ground-deep: "#0f0c09"
  panel: "#1a1511"
  panel-2: "#211b15"
  raised: "#29221b"
  line: "#362d24"
  line-soft: "#2a231c"
  text: "#f0e9df"
  text-2: "#b5a897"
  text-3: "#a8977f"
  ok: "#4ac08a"
  warn: "#e6b33f"
  bad: "#d64556"
  bad-ink: "#e86a76"
  bad-strong: "#b52f3c"
  info: "#7a9cd8"
typography:
  display:
    fontFamily: '"Inter Variable", ui-sans-serif, system-ui, sans-serif'
    fontSize: "2rem"
    fontWeight: 700
    lineHeight: 1.1
    letterSpacing: "-0.02em"
  headline:
    fontFamily: '"Inter Variable", ui-sans-serif, system-ui, sans-serif'
    fontSize: "1.5rem"
    fontWeight: 700
    lineHeight: 1.2
    letterSpacing: "-0.02em"
  title:
    fontFamily: '"Inter Variable", ui-sans-serif, system-ui, sans-serif'
    fontSize: "0.875rem"
    fontWeight: 600
    lineHeight: 1.2
  body:
    fontFamily: '"Inter Variable", ui-sans-serif, system-ui, sans-serif'
    fontSize: "0.9375rem"
    fontWeight: 400
    lineHeight: 1.6
  label:
    fontFamily: '"JetBrains Mono Variable", ui-monospace, "SF Mono", Menlo, Consolas, monospace'
    fontSize: "0.75rem"
    fontWeight: 600
    letterSpacing: "0.04em"
  data:
    fontFamily: '"JetBrains Mono Variable", ui-monospace, "SF Mono", Menlo, Consolas, monospace'
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.4
rounded:
  none: "0px"
  xs: "2px"
  sm: "4px"
  md: "6px"
  pill: "999px"
spacing:
  xs: "6px"
  sm: "8px"
  md: "12px"
  lg: "16px"
  xl: "22px"
  pane: "26px"
components:
  button-primary:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.primary-on}"
    typography: "{typography.title}"
    rounded: "{rounded.sm}"
    padding: "10px 16px"
  button-primary-hover:
    backgroundColor: "{colors.primary-hover}"
  button-primary-active:
    backgroundColor: "{colors.primary-press}"
  button-secondary:
    backgroundColor: "{colors.panel-2}"
    textColor: "{colors.text}"
    rounded: "{rounded.sm}"
    padding: "10px 16px"
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.text-2}"
    rounded: "{rounded.sm}"
    padding: "10px 16px"
  button-danger:
    backgroundColor: "{colors.bad-strong}"
    textColor: "#fdf1f0"
    rounded: "{rounded.sm}"
    padding: "10px 16px"
  icon-button:
    backgroundColor: "{colors.panel-2}"
    textColor: "{colors.text-2}"
    rounded: "{rounded.sm}"
    size: "34px"
  input:
    backgroundColor: "{colors.ground-deep}"
    textColor: "{colors.text}"
    rounded: "{rounded.sm}"
    padding: "9px 12px"
  panel:
    backgroundColor: "{colors.panel}"
    rounded: "{rounded.md}"
  badge-ok:
    backgroundColor: "rgba(74, 192, 138, 0.13)"
    textColor: "{colors.ok}"
    rounded: "{rounded.xs}"
    padding: "5px 9px"
  nav-item-active:
    backgroundColor: "rgba(226, 98, 44, 0.14)"
    textColor: "{colors.primary-ink}"
    rounded: "{rounded.sm}"
    padding: "9px 12px"
---

# Design System: tambo

## Overview

**Creative North Star: "The Waystation at Night"**

tambo is a control panel that feels like a dim machine room with one warm lamp on: a warm brown-tinted near-black ground, hairline borders doing the structural work, and a single committed madder-orange accent that appears only where the operator acts or a state demands attention. It is deliberately angular — a corner scale of 0/2/4/6 with 4px as the ceiling on controls and 6px on surfaces — so nothing reads pillowy or default-SaaS. Density is honest: rules, tabular numerals, and mono data columns, not floating cards.

The system is dark-primary. The light theme is the same dye lots dipped for daylight, not a separate palette: identical roles, one notch deeper accent. Depth is real but quiet — offset-plus-blur shadows only on overlays (dialog, select popover, toast); everything else stands on 1px hairlines against the ground.

Key Characteristics:
- Warm brown-black grounds (undyed alpaca), never blue-black slate.
- One accent: madder #e2622c; semantic ok/warn/bad/info stay clearly distinct from it.
- Radius scale 0/2/4/6, pill reserved for toggle tracks only.
- Inter for UI, JetBrains Mono for all data (paths, sizes, ports, pids, rates) with tabular numerals.
- Hand-drawn 1.5-stroke SVG icons only; no icon fonts, no third-party icon sets.
- Hairline borders convey structure; shadows are reserved for elevation events.

## Colors

Warm, restrained, dye-lot based: every dark value is brown-tinted, and the accent is the only saturated voice on screen.

### Primary
- **Madder** (#e2622c): the core accent. Primary buttons, active rail item tint, focus rings, chart strokes, links-as-accents, the lit doorway in the mark. Hover lightens (#ea7141 — "the lamp brightens, it does not sink"); press deepens (#d4571f).
- **Madder ink** (#f0854f): accent used as text on dark ground (7.1:1) — active nav labels, arrows, flagged tokens in command wells.
- **Burnt-umber ink** (#1f0e05): text on madder fills (5.4:1).

### Neutral
- **Undyed alpaca** (#14100c): page ground; **Console** (#0f0c09): recessed wells, inputs, consoles; **Panel** (#1a1511) and **Panel-2** (#211b15): raised surfaces and rails; **Raised** (#29221b): hover fills, progress tracks.
- **Line** (#362d24) / **Line-soft** (#2a231c): hairline borders and row separators.
- **Text** (#f0e9df) / **Text-2** (#b5a897) / **Text-3** (#a8977f): primary, secondary, tertiary tiers.

### Semantic
- **Ok** (#4ac08a): running/healthy, connected dot, completed rows.
- **Warn** (#e6b33f): queued state, caution callouts (dark values are AA-lifted for the brown ground).
- **Bad** (#d64556 / #e86a76 as text / #b52f3c filled destructive).
- **Info** (#7a9cd8): in-flight download progress, informational callouts.
Each semantic color travels with a `-soft` translucent twin for backgrounds and uses `color-mix` at 30–32% for its badge border.

### Named Rules
**The One Lamp Rule.** Madder is the only brand hue; on any screen it stays on actions, active state, and data accents — status colors never borrow from it.
**The Dye-Lot Rule.** Both themes are the same palette family: a role changes value between themes, never meaning.

## Typography

**Display/Headline/Body Font:** Inter Variable (with ui-sans-serif, system-ui fallbacks)
**Data/Label Font:** JetBrains Mono Variable (with ui-monospace fallbacks)

**Character:** Inter at tight negative tracking (-0.02em on display sizes) gives headings a machined compactness; JetBrains Mono carries every byte count, port, pid, path, and rate so data always looks like data.

### Hierarchy
- **Display** (700, 2rem/32px, -0.02em): stat readout values only (.stat .v), tabular-nums.
- **Headline** (700, 1.5rem/24px, -0.02em): pane titles.
- **Title** (600, 0.875rem/14px): panel titles, buttons, field labels.
- **Body** (400, 0.9375rem/15px, 1.6): default prose.
- **Label** (600, 0.75rem/12px, 0.04em, uppercase, mono): section labels and stat keys — always trailed by a hairline rule, never a colored kicker.
- **Data** (400, mono, 0.75–0.8125rem): filenames, commands, logs, table cells; `font-variant-numeric: tabular-nums` wherever digits align.

### Named Rules
**The Data Speaks Mono Rule.** If it is a filename, size, port, pid, rate, or command, it is JetBrains Mono. Prose never is.

## Layout

Fixed shell: 60px masthead over a 208px left rail + scrollable stage (CSS grid, `100dvh`). Pane content is padded 26px with a 1180px max width. Panels stack on a 14px gap rhythm; panel internals use a 16px body padding and a 14px head strip. Stat readouts use a 4-up `.stat-grid` whose cells are separated by 1px rules (the grid gap IS the line) — rules, not floating cards. The five-column `.dl-row` (1fr / 120px / 130px / 90px / 40px) is the canonical data-row anatomy. Responsive: at ≤1000px stat grids go 2-up; at ≤720px the shell reflows to masthead + stage + bottom tab rail and download rows collapse to name+action. Breakpoints: 1000px, 860px (masthead tag hidden), 720px.

## Elevation & Depth

Quietly structural: depth comes from tonal steps (ground → panel → panel-2 → raised) plus hairlines, not shadow. Shadows exist only for elevation events — overlays that leave the page plane.

### Shadow Vocabulary
- **shadow-1** (`0 1px 2px rgba(0,0,0,.5), 0 2px 8px rgba(0,0,0,.35)`): small lift.
- **shadow-2** (`0 2px 4px rgba(0,0,0,.5), 0 8px 24px rgba(0,0,0,.45)`): dialog, select popover, toast.
- **glow** (`0 0 0 1px accent-line, 0 2px 14px rgba(226,98,44,.25)`): accent-emphasized focus/lift.
- **scrim** (`rgba(15,12,9,.72)`): dialog overlay, theme-tinted.

### Named Rules
**The Hairlines-First Rule.** A border or a tonal step resolves separation; a box-shadow appears only when something genuinely floats above the page.

## Shapes

Angular, not pillowy: a strict 0/2/4/6 scale. 2px for chips, badges, wells, progress; 4px is the ceiling on controls (buttons, inputs, rail items, icon buttons); 6px on surfaces (panels, dialogs, popovers, stat grid). The pill (999px) is reserved for toggle tracks only. Badges carry a 6px square status dot (`border-radius: 0`) — the square dot is a signature: even the indicator follows the angular law. Icons are drawn on 1.5px strokes with rounded joins inside square-leaning frames.

## Components

### Buttons
- **Shape:** lightly square (4px radius), 15px icon at 8px gap.
- **Primary:** madder fill, burnt-umber ink, 600/14px, 10px 16px; hover brightens, press deepens and shifts down 1px.
- **Secondary:** panel-2 fill with hairline border; hover swaps border to accent-line and lifts fill to raised.
- **Ghost:** transparent → panel-2 wash on hover. **Danger:** filled #b52f3c or ghost with bad border and bad-soft hover. Disabled: 45% opacity.

### Badges
- **Style:** translucent semantic fill, semantic text, `color-mix` border, 6px square leading dot; warn and info dots blink (disabled under `prefers-reduced-motion`).

### Panels / Wells
- **Panel:** panel ground, 1px line, 6px radius; 14px head strip over a 16px body.
- **Well:** console ground (#0f0c09), 2px radius, mono 12px/1.7 — for commands, env, logs.

### Inputs / Fields
- **Style:** recessed console ground, hairline, 4px radius; hover lightens border to text-3.
- **Focus:** accent border + 3px accent-soft ring. Invalid: bad border; hint text turns bad-ink.

### Navigation
- Left rail of 14px-radius items, mono tabular counts right-aligned; active item wears accent-soft fill with madder-ink text (no side stripe). Mobile: bottom tab rail, icons over 12px labels.

### Signature: the lit doorway mark
Square 30px frame, geometric doorway glowing madder from within; on boot the glow animates up once (0 → 0.95 → 0.55). It is the brand's only ornament.

### Signature: stat readout strip
Four cells fused by 1px rules: mono uppercase key, 32px display value with mono unit suffix, mono footnote line that can shift ok/bad.

## Do's and Don'ts

### Do:
- Keep every corner on the 0/2/4/6 scale; 4px on controls, 6px on surfaces.
- Set all data in JetBrains Mono with tabular numerals; keep prose in Inter.
- Convey structure with 1px hairlines on the tinted neutrals (#362d24 / #2a231c).
- Use translucent semantic fills (`--t-*-soft`) for badges and callouts, `color-mix` at 30–32% for their borders.
- Darken the accent exactly one dip for the light theme (madder → #b8451a) rather than picking a new orange.
- Keep contrast at AA: accent-as-text uses #f0854f on dark, danger text #e86a76.

### Don't:
- Don't introduce rounded-full or >6px radii on any control or surface (pill is toggle tracks only).
- Don't use shadows for panel separation; that is hairline work.
- Don't add icon fonts or third-party icon glyphs; draw 1.5-stroke SVGs only.
- Don't tint grounds blue or grey; grounds are brown-tinted (#14100c family).
- Don't use the accent for status; ok/warn/bad/info have their own dye lots.
- Don't letter-case Inter as a faux label; labels are mono, uppercase, tracked 0.04em, trailed by a rule.
- Don't render gradient text, glyph icons, or hard neobrutalist offset shadows — the world has no use for them.
