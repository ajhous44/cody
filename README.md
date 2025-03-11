
# 🤖 Cody - Your AI Coding Assistant 
[![Star History Chart](https://api.star-history.com/svg?repos=ajhous44/cody&type=Date)](https://star-history.com/#ajhous44/cody&Date)

Welcome to Cody! An AI assistant designed to let you interactively query your codebase using natural language. By utilizing vector embeddings, chunking, and OpenAI's language models, Cody can help you navigate through your code in an efficient and intuitive manner. 💻
https://www.star-history.com/#ajhous44/cody&Date
![image](https://github.com/ajhous44/cody/assets/42582780/f2a62a20-663c-4ec1-b000-67257331fb12)
## LINK
https://www.loom.com/share/eba1d0dcee20430fbd412580d1c0ea0e?sid=4998cf6f-45b4-480d-b742-6f22f3a49dc3


Cody continuously updates its knowledge base every time you save a file, ensuring you have the most up-to-date information. You can customize your setup by specifying directories to ignore in the `ignore_list`.

## 🚀 Getting Started

1. Clone the repo
2. (Optionally) Setup virtual environment by running `pip install -m venv .venv` and then `pip install -r requirements.txt` in terminal from the root of your directory
3. Rename the `.local.env` file to `.env`` and replace `YOUR_API_KEY_HERE` with your OpenAI API Key.
4. Modify the `IGNORE_THESE` global var at the top of the script to specify directories and files you wish to exclude from monitoring. (You should comment out any large files like a virtual environment, cache, js libraries you have downloaded, etc...)
5. Run the script using Python: python cody.py and follow terminal for setup. It will prompt you for if you want to use text chat (terminal) or conversational (speech i/o). It will also warn you if you remove .env from the ignore list.

## 🎯 Features

- **File Monitoring**: Real-time monitoring of all files in your project's directory and subdirectories. 👀
- **Embedding-based Knowledge Base**: Create a knowledge base using OpenAI Embeddings. Cody collects the contents of all text and JSON files and adds them to this knowledge base. 📚
- **Interactive Q&A**: Listen to user inputs. Ask questions, and Cody will generate a response using the knowledge base. 🧠
- **Customizable**: Easily specify files or directories to ignore during monitoring.

## 🛠 Dependencies

- `dotenv`: Load variables from a `.env` file into the environment.
- `langchain-community`: A language processing library used for embeddings and vector storage. Previously `langchain`.
- `langchain_openai`: Provides the `OpenAIEmbeddings` functionality, integrating OpenAI models directly with langchain's architecture.
- `litellm`: Call all LLM APIs using the OpenAI format (https://github.com/BerriAI/litellm)
- `watchdog`: Monitor filesystem events in real-time.
- `openai`: Generate smart responses using OpenAI's language model.
- `speech_recognition`: Convert speech to text for voice interaction.
- `gtts`: Google Text-to-Speech library for generating audio from text.
- `pygame`: Library to play audio files.

## 💡 Usage

- To stop the script, type 'exit' or speak the word 'exit' and press enter. Cody will gracefully terminate the program.

### Configuring the Ignore List

Cody allows you to specify which files and directories should be ignored during file monitoring. This is particularly useful for excluding files that change frequently, are not relevant to your queries, or could contain sensitive information.

To customize your `ignore_list`, add patterns matching the files or directories you wish to exclude. Cody supports simple wildcard patterns for flexibility. Here are some examples to guide you:

#### Examples

- **Ignoring Specific Files**: If you want to ignore all `.env` files, you can add `*.env` to the ignore list.
    ```python
    IGNORE_THESE = ['*.env']
    ```

- **Ignoring Directories**: To ignore an entire directory, such as `node_modules` or a virtual environment directory like `.venv`, simply add the directory name.
    ```python
    IGNORE_THESE = ['node_modules', '.venv']
    ```

- **Ignoring File Extensions**: To ignore all files with a specific extension, such as `.log` or `.tmp`, use the wildcard pattern `*`.
    ```python
    IGNORE_THESE = ['*.log', '*.tmp']
    ```

- **Complex Patterns**: You can combine directory names and wildcards to ignore specific types of files within certain directories. For example, to ignore all `.md` files in the `docs` directory:
    ```python
    IGNORE_THESE = ['docs/*.md']
    ```

#### Tips for Configuring Your Ignore List

- **Review Regularly**: As your project evolves, so too may the files and directories you need to ignore. Regularly reviewing and updating your `ignore_list` can help ensure Cody's performance remains optimal.

- **Use Wildcards Wisely**: While wildcards offer powerful flexibility, they can also lead to unintentionally ignoring important files. Be specific in your patterns to avoid such issues.

- **Test Changes**: After updating your `ignore_list`, perform a few tests to ensure that the changes behave as expected, especially if using complex patterns.

By carefully configuring your `ignore_list`, you can tailor Cody to better suit your project's needs, enhancing both its efficiency and relevance to your coding tasks.


## ⚠️ Notes & Tips

- Cody uses the FAISS library for efficient similarity search in storing vectors. Please ensure you have sufficient memory available, especially when monitoring a large number of files.
- Additionally, be sure to monitor your OpenAI api usage. A helpful tip is to set a monthly spend limit inside of your OpenAI account to prevent anything crazy from happening. As an additional helper, it prints the number of tokens used in each call you make.
- "LIVE" coding questions. To use to it's full potential. I recommend opening a seperate terminal or even command prompt cd'ing into your project directory, and then launching python cody.py. Then place it split screen with your code in a small viewing window on the far left or right. This way, you can use a seperate terminal for actually running your code without worrying about Cody or having to run him (er... it) each time! This will still continue to update with each file save you do on any file so it always is using the latest data.

## Contributing

Contributions are welcome. Please submit a pull request or open an issue for any bugs or feature requests.

Happy Coding with Cody! 💡🚀🎉
