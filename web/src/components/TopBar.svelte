<script lang="ts">
  import {
    ChevronRight,
    HelpCircle,
    Plus,
    Radio,
    Search,
    User
  } from 'lucide-svelte'

  let {
    onNewCombo,
    onSearch
  }: {
    onNewCombo?: () => void
    onSearch?: (query: string) => void
  } = $props()

  let searchInput = $state('')

  function handleInput(e: Event) {
    const val = (e.target as HTMLInputElement).value
    searchInput = val
    if (onSearch) onSearch(val)
  }
</script>

<header class="h-14 bg-[#0d1017] border-b border-[#1e2330] px-6 flex items-center justify-between gap-4 flex-shrink-0 z-20">
  <!-- Left Breadcrumbs -->
  <div class="flex items-center gap-2 font-code text-xs">
    <div class="w-5 h-5 rounded flex items-center justify-center bg-[#ff5c35]/15 text-[#ff5c35]">
      <span class="font-bold text-[10px]">9R</span>
    </div>
    <span class="text-[#e1e2ea] font-medium">9Router</span>
    <ChevronRight class="w-3.5 h-3.5 text-[#636c7e]" />
    <span class="text-[#8e95a5]">Localhost Cluster</span>
  </div>

  <!-- Center Search Bar -->
  <div class="relative w-full max-w-md hidden md:flex items-center">
    <Search class="absolute left-3 w-4 h-4 text-[#636c7e] pointer-events-none" />
    <input
      type="text"
      placeholder="Search routes, model IDs, providers (Ctrl+K)..."
      value={searchInput}
      oninput={handleInput}
      class="w-full bg-[#131722] border border-[#232a3b] rounded-lg pl-9 pr-12 py-1.5 font-body text-xs text-[#e1e2ea] placeholder:text-[#636c7e] focus:outline-none focus:border-[#ff5c35] transition"
    />
    <span class="absolute right-2.5 px-1.5 py-0.5 rounded bg-[#1c2230] font-code text-[10px] text-[#8e95a5] pointer-events-none border border-[#232a3b]">
      ⌘K
    </span>
  </div>

  <!-- Right Action Cluster -->
  <div class="flex items-center gap-3">
    <!-- Cluster Healthy -->
    <div class="flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-[#10b981]/10 border border-[#10b981]/25 text-[#10b981] font-code text-[11px] font-semibold">
      <span class="w-1.5 h-1.5 rounded-full bg-[#10b981] animate-pulse"></span>
      <span>Cluster Healthy</span>
    </div>

    <!-- + New Combo Button -->
    <button
      type="button"
      onclick={() => onNewCombo && onNewCombo()}
      class="flex items-center gap-1.5 px-3.5 py-1.5 rounded-lg bg-[#ff5c35] hover:brightness-110 text-white font-body text-xs font-bold shadow-md shadow-[#ff5c35]/25 transition cursor-pointer"
    >
      <Plus class="w-4 h-4" />
      <span>New Combo</span>
    </button>

    <!-- User / Profile -->
    <div class="w-7 h-7 rounded-full bg-[#181d27] border border-[#2b3245] flex items-center justify-center text-[#8e95a5] cursor-pointer hover:text-white transition">
      <User class="w-3.5 h-3.5" />
    </div>
  </div>
</header>
