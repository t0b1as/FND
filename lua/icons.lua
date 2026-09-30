-- icons.lua – eigener, einheitlicher Symbolsatz (24er-Raster, Strich 1,8 px,
-- Farbe = currentColor). Ersetzt Emojis, die auf jedem Gerät anders aussehen.
-- Verwendung in Lua:  require("icons").svg("listings")
-- In JavaScript:      window.FUNDUS_ICONS.listings  (von render.lua eingebettet)
-- Hinweis: keine Prozentzeichen verwenden – die Symbole landen teils in
-- string.format-Vorlagen.
local M = {}

local P = {
  home         = '<path d="M3 11.5 12 4l9 7.5"/><path d="M5.5 10v9.5h13V10"/><path d="M10 19.5V14h4v5.5"/>',
  listings     = '<circle cx="9" cy="20" r="1.4"/><circle cx="17" cy="20" r="1.4"/><path d="M2.5 4H5l2.2 10.3c.15.7.75 1.2 1.5 1.2h8.7c.7 0 1.3-.5 1.5-1.2L20.5 8H6.1"/>',
  energy       = '<path d="M13 2.5 4.5 13.5H11l-1 8 8.5-11H12.2z"/>',
  certificates = '<circle cx="12" cy="9" r="5.5"/><path d="m8.8 13.6-1.3 7.9L12 19l4.5 2.5-1.3-7.9"/><path d="m10 9 1.5 1.5L14.5 7.5"/>',
  shop         = '<path d="M4 8h14"/><path d="m14.5 4.5 3.5 3.5-3.5 3.5"/><path d="M20 16H6"/><path d="m9.5 12.5-3.5 3.5 3.5 3.5"/>',
  jobs         = '<rect x="3" y="7" width="18" height="13" rx="2.2"/><path d="M9 7V5.2c0-.7.5-1.2 1.2-1.2h3.6c.7 0 1.2.5 1.2 1.2V7"/><path d="M3 12.5h18"/>',
  files        = '<path d="M3 7.2C3 6 4 5 5.2 5H9l2 2.2h7.8C20 7.2 21 8.2 21 9.4v7.4c0 1.2-1 2.2-2.2 2.2H5.2C4 19 3 18 3 16.8z"/>',
  shared       = '<circle cx="17.5" cy="5.5" r="2.5"/><circle cx="6.5" cy="12" r="2.5"/><circle cx="17.5" cy="18.5" r="2.5"/><path d="m8.7 10.8 6.6-4M8.7 13.2l6.6 4"/>',
  messenger    = '<path d="M20.5 11.5a8 8 0 0 1-11.7 7.1L4 20l1.3-4.4A8 8 0 1 1 20.5 11.5z"/><path d="M8.5 11.5h.01M12.5 11.5h.01M16.5 11.5h.01"/>',
  partner      = '<path d="M12 20s-7.5-4.6-7.5-10.2A4.3 4.3 0 0 1 12 7a4.3 4.3 0 0 1 7.5 2.8C19.5 15.4 12 20 12 20z"/>',
  wallet       = '<path d="M17 7V5.5C17 4.7 16.3 4 15.5 4H5.5A2.5 2.5 0 0 0 3 6.5"/><rect x="3" y="6.5" width="18" height="13.5" rx="2.5"/><path d="M21 11h-4a2 2 0 0 0 0 4h4"/>',
  peers        = '<circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3a13.5 13.5 0 0 1 0 18M12 3a13.5 13.5 0 0 0 0 18"/>',
  storage      = '<ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6"/><path d="M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/>',
  settings     = '<circle cx="12" cy="12" r="3"/><path d="M12 2.8v2.4M12 18.8v2.4M4.2 7.5l2 1.2M17.8 15.3l2 1.2M4.2 16.5l2-1.2M17.8 8.7l2-1.2"/><circle cx="12" cy="12" r="7"/>',
  star         = '<path d="m12 3.5 2.6 5.3 5.9.9-4.3 4.1 1 5.8L12 16.9l-5.2 2.7 1-5.8-4.3-4.1 5.9-.9z"/>',
}

local HEAD = '<svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'

function M.svg(name)
  local p = P[name]
  if not p then return "" end
  return HEAD .. p .. "</svg>"
end

-- JSON-Objekt {name: svg} für window.FUNDUS_ICONS
function M.json()
  local parts = {}
  for k, _ in pairs(P) do
    local s = M.svg(k):gsub('\\', '\\\\'):gsub('"', '\\"')
    parts[#parts + 1] = '"' .. k .. '":"' .. s .. '"'
  end
  return "{" .. table.concat(parts, ",") .. "}"
end

return M
