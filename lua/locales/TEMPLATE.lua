-- =============================================================================
--  locales/TEMPLATE.lua – Vorlage für neue Sprachen
--
--  Anleitung:
--    1. Diese Datei kopieren: cp TEMPLATE.lua fr.lua
--    2. Alle Werte übersetzen (Schlüssel NICHT verändern)
--    3. In i18n.lua den Code in SUPPORTED eintragen: { "de", "en", "fr" }
--    4. In i18n.lua in lang_menu_html das Label + Flagge eintragen:
--       labels: fr = "Français"  /  flags: fr = "🇫🇷"
--
--  Platzhalter %{name} und Plural-Schlüssel (key_plural) beibehalten.
-- =============================================================================

return {
  nav = {
    brand        = "Fundus",
    market       = "",   -- TRANSLATE
    energy       = "",
    certificates = "",
    jobs         = "",
    partner      = "",
    shop         = "",
    network      = "",
  },
  general = {
    loading        = "",
    error          = "",
    not_found      = "",
    save           = "",
    cancel         = "",
    delete         = "",
    edit           = "",
    back           = "",
    publish        = "",
    published      = "",
    publishing     = "",
    no_entries     = "",
    internal_error = "",
    node_offline   = "",  -- placeholder: %{reason}
  },
  home = {
    title          = "",
    node_id        = "",
    peers          = "",
    peer           = "",
    listings_count = "",
    energy_count   = "",
    own_addresses  = "",
  },
  listing = {
    title_page    = "",
    new           = "",
    col_id        = "",
    col_title     = "",
    col_price     = "",
    col_condition = "",
    col_created   = "",
    col_category  = "",
    detail_title  = "",
    price_range   = "",   -- placeholders: %{min} %{max}
    keywords      = "",
    condition = { new="", good="", acceptable="", broken="" },
    category  = { electronics="", furniture="", clothing="", vehicle="",
                  tools="", household="", sports="", other="" },
  },
  upload = {
    title             = "",
    step_images       = "",
    step_voice        = "",
    step_analyze      = "",
    step_form         = "",
    image_hint        = "",
    image_btn         = "",
    image_too_large   = "",  -- %{name} %{mb}
    image_wrong_type  = "",  -- %{name} %{type}
    mic_start         = "",
    mic_stop          = "",
    mic_hint          = "",
    mic_done          = "",
    mic_no_permission = "",
    mic_error         = "",  -- %{error}
    mic_unsupported   = "",
    voice_placeholder = "",
    llm_disabled      = "",
    llm_disabled_hint = "",
    analyze_btn       = "",
    analyzing         = "",
    analyze_error     = "",  -- %{reason}
    analyze_skipped   = "",
    field_title       = "",
    field_category    = "",
    field_condition   = "",
    field_price_from  = "",
    field_price_to    = "",
    field_description = "",
    field_keywords    = "",
    title_placeholder = "",
    desc_placeholder  = "",
    kw_placeholder    = "",
    needs_content     = "",
  },
  energy = {
    title         = "",
    count         = "",   -- %{n}
    count_plural  = "",
    col_id        = "",
    col_meter     = "",
    col_kwh       = "",
    col_lat       = "",
    col_lon       = "",
    col_timestamp = "",
    fee_title     = "",
    fee_desc      = "",
  },
  certificate = {
    title     = "",
    col_id    = "",
    col_type  = "",
    col_issued = "",
  },
  job = {
    title        = "",
    col_id       = "",
    col_title    = "",
    col_type     = "",
    col_posted   = "",
    type_offer   = "",
    type_request = "",
  },
  peers = {
    title        = "",
    own_node     = "",
    connected    = "",
    count        = "",   -- %{n}
    count_plural = "",
    none         = "",
  },
  error = {
    not_found    = "",
    server_error = "",
  },
  footer = {
    tagline = "",
  },

  tos = {
    title          = "",
    version_label  = "",
    effective_date = "",
    accept_notice  = "",
    btn_accept     = "",
    btn_decline    = "",
    language_note  = "",
  },

  partner = {
    title           = "",
    privacy_title   = "",
    privacy_text    = "",
    step_profile    = "",
    step_matches    = "",
    label_nickname  = "",
    label_gender    = "",
    label_age       = "",
    label_radius    = "",
    label_interests = "",
    label_bio       = "",
    label_seek_gender = "",
    btn_save        = "",
    btn_publish     = "",
    no_matches      = "",
    match_peer      = "",
    match_score     = "",
    match_distance  = "",
    match_common    = "",
  },

  shop = {
    title           = "",
    what_is_fnd     = "",
    fnd_explain     = "",
    step_amount     = "",
    label_amount    = "",
    label_addr      = "",
    label_rate      = "",
    label_you_pay   = "",
    label_source    = "",
    btn_order       = "",
    step_pay        = "",
    label_sol_addr  = "",
    btn_copy        = "",
    label_order_id  = "",
    label_status    = "",
    step_done       = "",
    done_text       = "",
    btn_again       = "",
    status_pending  = "",
    status_received = "",
    status_minting  = "",
    status_complete = "",
    status_failed   = "",
    status_expired  = "",
    err_amount      = "",
    err_addr        = "",
  },
}
