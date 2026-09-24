#!/usr/bin/env python3
"""Print one task and its stage boundary. Does not execute any task.
Usage: show-task.py R00 | R07.1 | B02.M
"""
from pathlib import Path
import re
import sys


def main() -> int:
    if len(sys.argv) != 2 or not re.fullmatch(r'(R\d{2}(?:\.[123])?|[BO]\d{2}(?:\.[MITV])?)', sys.argv[1]):
        print(__doc__, file=sys.stderr)
        return 2
    task = sys.argv[1]
    root = Path(__file__).resolve().parent.parent
    path = root / 'tasks' / (task.replace('.', '-') + '.md')
    if not path.is_file():
        print(f'Unknown task: {task}', file=sys.stderr)
        return 2
    print('只输出任务文本，没有执行命令。规则见05-model-instructions.md。\n')
    print(path.read_text(encoding='utf-8'))
    if '.' in task:
        parent = root / 'tasks' / (task.split('.')[0] + '.md')
        print('\n---\n以下父卡仅提供上下文；本次仍只执行上面指定阶段。\n')
        print(parent.read_text(encoding='utf-8'))
    return 0

if __name__ == '__main__':
    sys.exit(main())
