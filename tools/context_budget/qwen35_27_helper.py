"""Explicit pinned 27B entry point; same isolated CPU transport/counter as 9B."""
from pathlib import Path
import sys
sys.path.insert(0, str(Path(__file__).resolve().parent))
from qwen35_helper import main
from qwen35_profile import PROFILE27
if __name__ == "__main__":
    sys.exit(main(PROFILE27))
