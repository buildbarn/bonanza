const fs = require('node:fs/promises')
const node = require('@tailwindcss/node')
const process = require('node:process');

async function main() {
  let compiler = await node.compile(`
    @import 'tailwindcss';
    @plugin "daisyui" {
      themes: light --default;
    }

    .bg-neutral {
      background-color: var(--color-neutral);
    }

    .message-contents ul {
      border-left: 1px dotted #555;
      list-style: none;
      padding-inline-start: 25px;
    }

    .message-contents table {
      width: 100%;
    }

    .message-contents tr {
      border-bottom: 1px dotted #555;
    }

    .message-contents tr th {
      padding: 5px;
      text-align: left;
    }

    .message-contents tr td {
      padding: 5px;
      text-wrap: nowrap;
    }
  `, {
    base: process.cwd(),
    onDependency(path) {},
  })
  let compiledCss = compiler.build([
    '[--tab-bg:var(--color-neutral)]',
    'alert-error',
    'alert-warning',
    'alert',
    'badge',
    'badge-primary',
    'bg-amber-100',
    'bg-base-100',
    'bg-base-200',
    'bg-neutral',
    'bg-primary',
    'block',
    'border',
    'border-base-300',
    'break-all',
    'btn-active',
    'btn-disabled',
    'btn-ghost',
    'btn-outline',
    'btn-primary',
    'btn-square',
    'btn',
    'card-actions',
    'card-body',
    'card-title',
    'card',
    'flex-col',
    'flex',
    'float-right',
    'font-mono',
    'h-auto!',
    'join',
    'join-item',
    'justify-between',
    'justify-end',
    'inline-block',
    'link',
    'link-accent',
    'link-primary',
    'link',
    'list-disc',
    'list-inside',
    'max-w-[100rem]',
    'm-4',
    'mb-4',
    'message-contents',
    'mt-4',
    'mx-auto',
    'my-2',
    'my-4',
    'navbar',
    'overflow-x-auto',
    'overflow-x-hidden',
    'p-4',
    'rounded-box',
    'shadow-sm',
    'shadow',
    'space-x-4',
    'space-y-4',
    'tab-active',
    'tab-content',
    'tab',
    'table-fixed',
    'table-pin-cols',
    'table',
    'tabs-lift',
    'tabs',
    'text-2xl',
    'text-amber-200',
    'text-center',
    'text-fuchsia-300',
    'text-gray-500',
    'text-left',
    'text-neutral-content!',
    'text-neutral-content',
    'text-primary-content',
    'text-red-600',
    'text-right',
    'text-sm',
    'text-xl',
    'text-xs',
    'textarea',
    'w-1/3',
    'w-1/4',
    'w-2/3',
    'w-3/4',
    'w-full',
    'whitespace-nowrap',
  ])
  let optimizedCss = node.optimize(compiledCss, { minify: true })
  await fs.writeFile(process.argv[2], optimizedCss)
}

main()
